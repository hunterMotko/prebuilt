# Logging and Backups

What this system records, what it keeps, for how long, and why. Both concerns
are documented together because they run from the same nightly job and compete
for the same scarce resource: disk on a small VPS.

Last updated 2026-07-26.

---

## Guiding principle

Logs and backups answer different questions and deserve opposite defaults.

| | Logs | Backups |
|---|---|---|
| Question | "Why did that fail?" | "Can we get it back?" |
| Value of one record | Near zero | Total |
| Volume | High | Low |
| Default | Discard aggressively | Keep redundantly |
| Cost of losing it | An unexplained incident | The business |

Treating logs like backups fills the disk. Treating backups like logs loses the
company's customer list. The rules below follow from that split.

---

# Part 1 — Logging

## Three log streams, three owners

| Stream | Written by | Location | Rotation | Retention |
|---|---|---|---|---|
| HTTP access | nginx | `/var/log/nginx/access.log` | logrotate (distro default) | 14 days, compressed |
| nginx errors | nginx | `/var/log/nginx/error.log` | logrotate | 14 days |
| Application | Go app → stdout | Docker `json-file` | 10 MB × 5 files | ~50 MB rolling |
| Error digest | `scripts/maintenance.sh` | `backups/errors-YYYY-MM-DD.log` | nightly | 14 days |

nginx is the **system of record for raw traffic**. The application log is
deliberately not a second copy of it.

## What the application logs

As of 2026-07-26 the app no longer uses Echo's default request logger. It uses
`errorOnlyLogger()` in `logging.go`, which emits one JSON line per **failed**
request and nothing else.

**Logged:**

| Category | Status | Level | Why it matters |
|---|---|---|---|
| `server_error` | 5xx | error | A real bug. Should be zero; investigate every one |
| `auth_failed` | 401, 403 | warn | Credential guessing against `/admin` |
| `rate_limited` | 429 | warn | Abuse, scraping, or a broken client |
| `payload_too_large` | 413 | warn | Oversized upload, possibly probing limits |
| `bad_request` | 400 | warn | Includes rejected CSRF tokens |
| `validation_failed` | 422 | info | Usually a real person mis-filling a form |

Plus 20 explicit `c.Logger().Error(...)` call sites in handlers for database and
filesystem failures.

**Not logged, on purpose:**

- **All 2xx/3xx responses.** nginx already has them. Duplicating meant two disk
  writes per request for identical facts.
- **All static asset requests.** One page view pulls CSS, JS, logo, and photos;
  with `Cache-Control: no-cache` browsers revalidate each on every visit. This
  was the majority of log volume and none of the signal.
- **404s.** nginx records them, and unmatched-route noise from bot scanners is
  the bulk. Scanning is better handled where the data already lives — fail2ban
  reads nginx's access log directly.
- **Request bodies and form values.** These hold customer names, phone numbers,
  and email addresses. PII in logs is subject to no retention policy, gets
  copied into digests and backups, and is a liability with no diagnostic payoff.

## Sample line

```json
{"time":"2026-07-26T21:54:49Z","level":"warn","category":"auth_failed",
 "status":401,"method":"GET","path":"/admin","ip":"::1","latency_ms":0,
 "user_agent":"curl/8.7.1","error":"code=401, message=Unauthorized"}
```

Categories are stable strings intended as grep keys:

```bash
docker logs prebuilt-sheds | grep '"category":"server_error"'
docker logs prebuilt-sheds | grep '"category":"auth_failed"' | jq -r .ip | sort | uniq -c | sort -rn
```

The second is the practical way to spot a brute-force attempt: one IP appearing
dozens of times.

## Why not log everything and filter later?

That is the right answer with centralised logging, where storage is elastic and
queries run elsewhere. It is the wrong answer on a 1–2 GB VPS where the disk
holding logs also holds the SQLite database — a full disk stops writes, which
**silently stops lead capture**. Sampling at the source is the correct tradeoff
until there is somewhere to ship logs to.

## Best practices applied

- **Structured, not prose.** JSON lines are greppable and machine-parseable
  without regex archaeology.
- **Stable categories.** Search by intent (`auth_failed`), not by matching text
  that changes when a message is reworded.
- **One writer, one mutex.** Entries are marshalled then written with a single
  `log.Print`; concurrent requests cannot interleave partial lines.
- **stdout, not files.** The container writes to stdout and lets Docker own
  rotation. Apps that manage their own log files re-implement logrotate badly
  and break when the disk fills.
- **Client IP via `c.RealIP()`**, which reads `X-Forwarded-For` because
  `TRUST_PROXY=true` installs the extractor. Without that, every log line would
  read as nginx's own address.
- **No PII.** See above.

## Known gaps

- **No alerting.** Nothing pages anyone on a 5xx spike. The nightly digest is a
  poor substitute for real alerting (`OPS-5` in `BACKLOG.md`).
- **No request IDs**, so a customer report cannot be correlated to a specific
  log line. `middleware.RequestID` would fix this cheaply.
- **No retention policy on nginx logs beyond logrotate's default**, and those
  contain full IPs — worth aligning with the privacy policy when written
  (`LEGAL-1`, `LEGAL-2`).

---

# Part 2 — Backups

## What is protected

| Data | Where it lives | Recoverable elsewhere? | Priority |
|---|---|---|---|
| `contact_submissions` | SQLite volume | Partially — each also emails the owner | **Critical** |
| `inventory_items`, colours | SQLite volume | No | High |
| Uploaded photos | uploads volume | No — originals may only exist here | High |
| Templates, CSS, Go source | git | Yes, fully | None |
| `.env` secrets | VPS only, `chmod 600` | **No** | **Manual** |

Two things deserve emphasis.

**`.env` is not backed up by anything.** It is gitignored by design and the
maintenance script does not touch it, because encrypting secrets into a backup
that is synced off-box needs a decision nobody has made yet. Losing the VPS
today means re-creating the SMTP app password and admin credentials by hand.
That is survivable but should be a conscious choice — store a copy in a password
manager.

**Photos have no second copy anywhere.** The contact table at least has the
email trail as a partial fallback. Photos do not. Once the admin panel is in
regular use, this is the highest-value data on the server.

## What runs, and when

`scripts/maintenance.sh`, nightly at 03:15:

```
15 3 * * * cd /srv/prebuilt && nice -n 19 ionice -c3 ./scripts/maintenance.sh >> backups/maintenance.log 2>&1
```

| Step | Method | Retention |
|---|---|---|
| Database snapshot | `sqlite3 .backup` → gzip | 14 daily snapshots |
| Photo mirror | incremental `cp -n` from the volume | Never pruned |
| Error digest | last 24h of 4xx/5xx | 14 days |
| Disk check | warn at 80% | — |

## Why each choice

**`sqlite3 .backup`, never `cp`.** The online backup API produces a
point-in-time consistent snapshot while the app keeps serving. Copying a live
database file can capture a torn write — a backup that restores into a corrupt
database, which you discover at the worst possible moment.

**The database is gzipped; photos are not.** Measured on real data:

| | Raw | gzip -9 | Verdict |
|---|---|---|---|
| `prebuilt.db` | 45,056 B | 1,905 B | 96% smaller — compress it |
| A shed JPEG | 235,025 B | 233,978 B | 0.4% — pure wasted CPU |

JPEG, PNG, and WebP are already compressed. Gzipping them spends 100% of the CPU
for none of the benefit, which matters on a shared 1-vCPU box.

**Photos mirror incrementally, they do not re-archive.** Uploaded filenames are
random and never rewritten, so `cp -n` copies only genuinely new files. A
nightly tarball would rewrite every photo every night forever — the cost grows
with total library size rather than with what changed.

**Database snapshots are full copies, not incremental.** At ~2 KB compressed
there is no reason to be clever, and every snapshot restores standalone with no
chain to reconstruct.

**The photo mirror is never pruned.** A photo deleted by a mis-click in the
admin panel stays recoverable. Deletion there is currently permanent and
cascades to disk (`BUG-9`), so this mirror is the only undo that exists.

**`nice -n 19 ionice -c3`.** The job yields CPU and disk to request handling
even under load, so a backup running during a traffic spike cannot slow the
site.

## Restore procedure

```bash
# 1. Pick a snapshot
ls -lh backups/db/

# 2. Decompress to a scratch copy — never straight over the live database
gunzip -c backups/db/prebuilt-2026-07-26.db.gz > /tmp/restore.db

# 3. Verify BEFORE trusting it
sqlite3 /tmp/restore.db "PRAGMA integrity_check;"        # expect: ok
sqlite3 /tmp/restore.db "SELECT count(*) FROM contact_submissions;"

# 4. Stop the app, replace, restart
docker compose stop app
docker cp /tmp/restore.db prebuilt-sheds:/data/prebuilt.db
docker compose start app
```

Photos restore by copying `backups/uploads/` back into the uploads volume.

## Best practices applied

- **3-2-1, partially.** Three copies (live, local snapshot, off-box), two media,
  one off-site. The off-box leg is **not yet enabled** — see gaps below.
- **Consistent snapshots**, not file copies.
- **Verify on restore**, via `PRAGMA integrity_check` before trusting a file.
- **Fail loudly.** The script exits non-zero if the container is missing. The
  previous `backup.sh` aborted with "prebuilt.db not found" into a cron log
  nobody read — the failure mode this replaced.
- **Retention matched to value.** Fourteen daily database snapshots; photos
  forever; logs briefly.

## Known gaps — read before trusting this

1. **The off-box copy is not enabled.** Every backup currently sits on the same
   disk as the data it protects, which does not survive that disk failing. This
   is the single largest remaining risk. `rclone config` needs an interactive
   browser login only the owner can run; the commented `rclone sync` line is
   ready at the bottom of the script.
2. **No restore has ever been tested.** A backup that has never been restored is
   a hope, not a backup. Run the procedure above once against a scratch copy.
3. **The crontab entry is not installed yet.** The script exists; nothing is
   running it.
4. **`.env` is not covered.** Copy it into a password manager.
5. **No monitoring of the backup itself.** A job that stops running fails
   silently. Once alerting exists (`OPS-5`), alert on the absence of a fresh
   snapshot — the classic failure is discovering the cron job died months ago.
