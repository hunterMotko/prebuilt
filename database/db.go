package database

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var DB *sql.DB

// checkDatabaseDir fails fast, and specifically, when the database's directory
// is missing or read-only. Both cases otherwise surface as SQLite's CANTOPEN,
// which the driver renders as "out of memory" — the single most misleading
// error in this deployment. In Docker the usual cause is the bind-mounted data
// directory still being owned by root while the container runs as uid 10001.
func checkDatabaseDir(path string) {
	dir := filepath.Dir(path)
	abs, _ := filepath.Abs(dir)

	info, err := os.Stat(dir)
	if err != nil {
		log.Fatalf("database directory %s does not exist (or is unreadable): %v", abs, err)
	}
	if !info.IsDir() {
		log.Fatalf("database directory %s is not a directory", abs)
	}

	// Stat can't tell us whether *this* user may write here — ownership and
	// mode have to be combined with the effective uid. Actually creating a file
	// is the only answer that's never wrong.
	probe := filepath.Join(dir, ".write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatalf("database directory %s is not writable by uid %d: %v\n"+
			"In Docker this usually means the bind-mounted data directory is still owned by "+
			"root — fix with: sudo chown -R 10001:10001 data",
			abs, os.Geteuid(), err)
	}
	f.Close()
	if err := os.Remove(probe); err != nil {
		log.Printf("warning: could not remove write probe %s: %v", probe, err)
	}
}

// Init opens the database at path and brings the schema up to date.
//
// The path is a parameter rather than an os.Getenv read so callers — including
// tests — can point at a temporary file without mutating the environment. It
// must sit inside a writable *directory*: SQLite puts its rollback-journal and
// WAL sidecars alongside the database, so bind-mounting just the .db file would
// leave those sidecars in a container's ephemeral layer where a crash mid-write
// could not be rolled back.
//
// Failures here call log.Fatal rather than returning an error, deliberately.
// Every one of them is a startup-time condition — missing directory, unwritable
// volume, failed migration — where continuing would serve a broken site, and
// the caller's only sane response is to exit with the same message.
func Init(path string) {
	var err error

	// Checked up front because SQLite reports both "directory missing" and
	// "directory not writable" as the same opaque CANTOPEN error, whose driver
	// text reads "unable to open database file: out of memory (14)". That
	// message sends you hunting for a memory problem that doesn't exist, so
	// diagnose the real cause here instead.
	checkDatabaseDir(path)

	DB, err = sql.Open("sqlite", path)
	if err != nil {
		log.Fatal("failed to open database:", err)
	}

	// SQLite's PRAGMA foreign_keys is per-connection, and database/sql pools
	// connections — a pragma set on one connection wouldn't reliably apply
	// to others the pool opens later. Pinning to a single connection makes
	// the pragma (and inventory_images' ON DELETE CASCADE) actually stick.
	// SQLite serializes writes anyway, so this costs nothing in practice for
	// a small low-traffic site.
	DB.SetMaxOpenConns(1)

	if err = DB.Ping(); err != nil {
		abs, _ := filepath.Abs(path)
		log.Fatalf("failed to open the database at %s: %v\n"+
			"SQLite's \"out of memory\" here is misleading — it means the file could not be "+
			"opened or created. Check that the directory exists and is writable by this user.",
			abs, err)
	}

	if _, err := DB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		log.Fatal("failed to enable foreign keys:", err)
	}

	// Without this, a write that finds the database locked fails INSTANTLY with
	// SQLITE_BUSY instead of waiting. SetMaxOpenConns(1) above serialises this
	// process's own access, so the lock contention that matters comes from
	// outside it: scripts/maintenance.sh runs `.backup` against the live
	// database nightly, and `sqlite3 prebuilt.db` is the documented way to edit
	// the colour reference tables. Either can hold a lock for long enough that
	// a contact form submitted at that moment would have errored and told a
	// real customer to call instead.
	//
	// Five seconds is far longer than any operation here legitimately takes, so
	// in practice this converts a rare hard failure into an unnoticed pause.
	if _, err := DB.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		log.Fatal("failed to set busy_timeout:", err)
	}

	// WAL, set after busy_timeout so the mode switch itself can wait for a lock
	// rather than failing outright.
	//
	// In SQLite's default rollback-journal mode a writer escalates to an
	// EXCLUSIVE lock at COMMIT, and for that window no other process can read.
	// The window is short — a transaction holds only a RESERVED lock until it
	// commits, and readers are fine during that part — which is precisely why
	// the resulting failure is intermittent rather than obvious.
	//
	// It took a red CI run on main to expose one: POST /contact returns as soon
	// as the row is saved, then a background goroutine writes the email status.
	// A reader landing in that commit window got SQLITE_BUSY, because
	// busy_timeout is per connection and a separate process gets SQLite's
	// default of 0, meaning "fail immediately".
	//
	// Measured against this exact topology — one long-lived writer connection,
	// as the pool holds here, plus short-lived reader processes — 200 reads at
	// timeout 0 lost 19 times under rollback-journal and 0 times under WAL.
	//
	// The same measurement with short-lived WRITER processes inverts: WAL loses
	// more, because the last connection to close attempts a checkpoint and needs
	// exclusive access. That is not this server, which holds one connection for
	// the process lifetime, but it does apply to the documented habit of editing
	// the colour tables with `sqlite3 prebuilt.db` — occasional manual use, so
	// it costs nothing here, but it is the reason WAL is not a free win in
	// general.
	//
	// Unlike foreign_keys and busy_timeout, journal_mode is a persistent
	// property of the file, not the connection, so this survives restarts and
	// applies to every process that opens the database — including the sqlite3
	// CLI. Queried rather than Exec'd because the statement RETURNS the mode
	// actually in force, and SQLite reports success while silently keeping the
	// old mode if the switch cannot be made.
	var journalMode string
	if err := DB.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&journalMode); err != nil {
		log.Fatal("failed to set journal_mode:", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		log.Fatalf("journal_mode is %q, not WAL — the database directory must be on a "+
			"filesystem that supports shared memory (a bind mount of the .db file alone, "+
			"or a network filesystem, will do this)", journalMode)
	}

	createTables()
	createInventoryTables()
}

// Email delivery states recorded against each submission.
//
// "Sent" means the SMTP server accepted the message for delivery — not that it
// reached an inbox. Nothing an outbound SMTP client can observe proves final
// receipt, so the admin UI says "accepted by mail server", never "received".
const (
	EmailPending = "pending" // saved; the send has not finished yet
	EmailSent    = "sent"    // SMTP accepted it
	EmailFailed  = "failed"  // SMTP returned an error — needs a human
	EmailSkipped = "skipped" // SMTP not configured, so no attempt was made
)

func createTables() {
	query := `
	CREATE TABLE IF NOT EXISTS contact_submissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		phone TEXT NOT NULL,
		email TEXT NOT NULL,
		style TEXT NOT NULL,
		size TEXT NOT NULL,
		details TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		email_status TEXT NOT NULL DEFAULT 'pending',
		email_error TEXT NOT NULL DEFAULT '',
		email_attempted_at DATETIME
	);`

	if _, err := DB.Exec(query); err != nil {
		log.Fatal("failed to create tables:", err)
	}

	migrateEmailDeliveryColumns()
}

// migrateEmailDeliveryColumns adds delivery tracking to databases created
// before it existed. Plain ALTER TABLE ADD COLUMN is enough here — unlike a
// CHECK constraint, SQLite supports adding columns directly, so no table
// rebuild is needed. Idempotent: each column is checked before being added.
//
// Rows that predate this migration get 'pending' from the column default. That
// is honest — those submissions were sent (or not) before anything recorded the
// outcome, and claiming 'sent' for them would be inventing history.
func migrateEmailDeliveryColumns() {
	columns := []struct{ name, ddl string }{
		{"email_status", `ALTER TABLE contact_submissions ADD COLUMN email_status TEXT NOT NULL DEFAULT 'pending'`},
		{"email_error", `ALTER TABLE contact_submissions ADD COLUMN email_error TEXT NOT NULL DEFAULT ''`},
		{"email_attempted_at", `ALTER TABLE contact_submissions ADD COLUMN email_attempted_at DATETIME`},
	}

	for _, col := range columns {
		exists, err := columnExists("contact_submissions", col.name)
		if err != nil {
			log.Fatal("failed to inspect contact_submissions schema:", err)
		}
		if exists {
			continue
		}
		if _, err := DB.Exec(col.ddl); err != nil {
			log.Fatalf("failed to add %s column: %v", col.name, err)
		}
	}
}

type ContactSubmission struct {
	ID        int64
	Name      string
	Phone     string
	Email     string
	Style     string
	Size      string
	Details   string
	CreatedAt time.Time

	EmailStatus      string
	EmailError       string
	EmailAttemptedAt sql.NullTime
}

func SaveContactSubmission(s ContactSubmission) (int64, error) {
	res, err := DB.Exec(
		`INSERT INTO contact_submissions (name, phone, email, style, size, details) VALUES (?, ?, ?, ?, ?, ?)`,
		s.Name, s.Phone, s.Email, s.Style, s.Size, s.Details,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MarkEmailStatus records the outcome of the delivery attempt for a submission.
// errMsg is stored verbatim so the admin sees the actual SMTP failure ("535
// authentication failed", "connection refused") rather than a generic message
// that gives no clue what to fix.
func MarkEmailStatus(id int64, status, errMsg string) error {
	_, err := DB.Exec(
		`UPDATE contact_submissions
		 SET email_status = ?, email_error = ?, email_attempted_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		status, errMsg, id,
	)
	return err
}

const submissionSelect = `
	SELECT id, name, phone, email, style, size, details, created_at,
	       email_status, email_error, email_attempted_at
	FROM contact_submissions`

// ListContactSubmissions returns the most recent submissions, newest first.
func ListContactSubmissions(limit int) ([]ContactSubmission, error) {
	rows, err := DB.Query(submissionSelect+` ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []ContactSubmission
	for rows.Next() {
		var s ContactSubmission
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Phone, &s.Email, &s.Style, &s.Size, &s.Details, &s.CreatedAt,
			&s.EmailStatus, &s.EmailError, &s.EmailAttemptedAt,
		); err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

// CountUndeliveredSubmissions counts submissions whose notification email was
// not confirmed as accepted by the mail server. Drives the admin warning
// banner, so it deliberately counts 'pending' and 'skipped' alongside 'failed':
// all three mean "this lead may exist only in the database".
func CountUndeliveredSubmissions() (int, error) {
	var n int
	err := DB.QueryRow(
		`SELECT count(*) FROM contact_submissions WHERE email_status != ?`, EmailSent,
	).Scan(&n)
	return n, err
}
