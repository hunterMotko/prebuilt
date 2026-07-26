package database

import (
	"database/sql"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

var DB *sql.DB

func Init() {
	var err error

	// DB_PATH lets the database file live outside the working directory, which
	// a container needs: SQLite writes its rollback-journal/WAL sidecar files
	// into the same directory as the database, so the database has to sit in a
	// mounted *directory*. Bind-mounting just the .db file would leave those
	// sidecars in the container's ephemeral layer, where a crash mid-write
	// couldn't be rolled back. Defaults to the original path so a plain
	// `go run .` behaves exactly as before.
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "./prebuilt.db"
	}

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
		log.Fatal("failed to connect to database:", err)
	}

	if _, err := DB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		log.Fatal("failed to enable foreign keys:", err)
	}

	createTables()
	createInventoryTables()
}

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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	if _, err := DB.Exec(query); err != nil {
		log.Fatal("failed to create tables:", err)
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
