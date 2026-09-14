#!/bin/sh
# Nightly maintenance: consistent database snapshot, incremental photo mirror,
# retention pruning, and an error digest.
#
# This script produces the snapshots. It does NOT send them anywhere — the
# off-box copy is a PULL, run from the operator's own machine (see
# `docs/LOGGING_AND_BACKUPS.md`). That split is deliberate:
#
#   - The privileged half stays on the box, where the privilege already is.
#     Reaching the database means reaching the Docker daemon, which is
#     root-equivalent; a push would need outbound credentials stored here too.
#   - The pulling half needs no new privilege at all, so the operator's job is
#     a plain file copy — the part most likely to run unattended for months
#     without anyone inspecting it.
#   - They fail independently, so a broken snapshot and a broken transfer are
#     distinguishable rather than one silent "no backups" state.
#
# Retention is what makes an irregular pull safe: KEEP_DAYS of snapshots stay
# here, so a laptop that was closed for a week loses nothing — the next pull
# collects everything it missed. At ~2 KB per snapshot that costs nothing.
#
# Written for a small VPS. The expensive-looking parts are avoided on purpose:
#
#   - The database is dumped with SQLite's online backup API, not copied. A
#     plain `cp` of a live database can capture a torn write.
#   - The database IS gzipped: it is mostly text and compresses ~96%
#     (measured: 45 KB -> 1.9 KB).
#   - Photos are NOT gzipped. They are already JPEG/PNG/WebP; compressing them
#     measured 235,025 -> 233,978 bytes, a 0.4% gain for 100% of the CPU cost.
#   - Photos are mirrored incrementally rather than re-archived nightly.
#     Uploaded filenames are random and never rewritten, so `cp -n` copies only
#     genuinely new files. A nightly tarball would rewrite every photo forever.
#
# RUN AS ROOT. Two things need it: the Docker socket (root-equivalent, so the
# deploy user is deliberately not in the docker group), and reading the uploads
# volume directly. Install in root's crontab with absolute paths on both sides —
# the script self-locates, so the path only has to find it, but a relative log
# path under cron fails silently:
#
#   sudo crontab -e
#   15 3 * * * BACKUP_OWNER=youruser nice -n 19 ionice -c3 /path/to/repo/scripts/maintenance.sh >> /path/to/repo/backups/maintenance.log 2>&1
#
# BACKUP_OWNER is what makes the pull possible: root writes these files, so
# without it the operator cannot read their own backups without sudo. Set it to
# the account the pull authenticates as. Left empty, ownership is untouched.
#
# nice/ionice keep this off the critical path: even under load it yields CPU
# and disk to request handling rather than competing with it.

set -eu

cd "$(dirname "$0")/.."

BACKUP_DIR=${BACKUP_DIR:-backups}
KEEP_DAYS=${KEEP_DAYS:-14}
CONTAINER=${CONTAINER:-prebuilt-sheds}
BACKUP_OWNER=${BACKUP_OWNER:-}
STAMP=$(date +%Y-%m-%d)

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

if ! mkdir -p "$BACKUP_DIR/db" "$BACKUP_DIR/uploads" 2>/dev/null; then
	log "ERROR: cannot create '$BACKUP_DIR' — check the path and its ownership"
	exit 1
fi

# Capture stderr separately: without this, a permission-denied on the docker
# socket is indistinguishable from a missing container, and the error message
# blames the wrong thing.
if ! INSPECT_ERR=$(docker inspect "$CONTAINER" 2>&1 >/dev/null); then
	case "$INSPECT_ERR" in
	*[Pp]ermission\ denied*)
		log "ERROR: cannot reach the docker daemon (permission denied) — run via sudo or from root's crontab"
		;;
	*)
		log "ERROR: container '$CONTAINER' not found — is the stack running?"
		;;
	esac
	exit 1
fi

# ── 1. Database ───────────────────────────────────────────────────────────────
# .backup is SQLite's online backup: a point-in-time consistent snapshot taken
# while the app keeps serving requests. No downtime, no write lock held open.
# This is the whole reason the pull cannot simply rsync the live database file.
log "backing up database"
docker exec "$CONTAINER" sqlite3 /data/prebuilt.db ".backup '/tmp/snapshot.db'"
docker cp "$CONTAINER:/tmp/snapshot.db" "$BACKUP_DIR/db/prebuilt-$STAMP.db"
docker exec "$CONTAINER" rm -f /tmp/snapshot.db
gzip -9 -f "$BACKUP_DIR/db/prebuilt-$STAMP.db"
log "database backed up ($(du -h "$BACKUP_DIR/db/prebuilt-$STAMP.db.gz" | cut -f1))"

# ── 2. Photos ─────────────────────────────────────────────────────────────────
# The volume name is read from the running container rather than hardcoded,
# because Compose prefixes it with the project directory name.
#
# Copied straight from the volume's mountpoint rather than through a helper
# container. Running as root, the path is readable directly, so the previous
# `docker run alpine` round-trip bought nothing and added a registry pull to a
# job that must keep working when the registry is unreachable.
UPLOAD_VOL=$(docker inspect -f \
	'{{range .Mounts}}{{if eq .Destination "/app/public/images/inventory"}}{{.Name}}{{end}}{{end}}' \
	"$CONTAINER")

if [ -z "$UPLOAD_VOL" ]; then
	log "WARNING: no uploads volume found on $CONTAINER — skipping photos"
else
	UPLOAD_PATH=$(docker volume inspect -f '{{.Mountpoint}}' "$UPLOAD_VOL")
	if [ ! -r "$UPLOAD_PATH" ]; then
		# Named volumes are root-owned. Say so rather than mirroring nothing and
		# reporting success — a silently empty photo backup is the failure you
		# only discover when you need it.
		log "WARNING: $UPLOAD_PATH is not readable (run as root) — skipping photos"
	else
		log "mirroring photos from volume $UPLOAD_VOL"
		cp -a -n "$UPLOAD_PATH/." "$BACKUP_DIR/uploads/" 2>/dev/null || true
		log "photo mirror now $(du -sh "$BACKUP_DIR/uploads" | cut -f1)"
	fi
fi

# ── 3. Retention ──────────────────────────────────────────────────────────────
# Each database snapshot is a full, self-contained copy, so deleting old ones
# never affects the ability to restore from a newer one. KEEP_DAYS therefore
# also sets how long the off-box pull may go without running.
#
# The photo mirror is deliberately NOT pruned: a photo deleted in the admin
# panel by mistake stays recoverable here.
find "$BACKUP_DIR/db" -name 'prebuilt-*.db.gz' -mtime "+$KEEP_DAYS" -delete
log "pruned database snapshots older than $KEEP_DAYS days"

# ── 4. Error digest ───────────────────────────────────────────────────────────
# Container logs rotate away (10 MB x 5) and nobody reads them. This pulls just
# the 4xx/5xx responses and error lines from the last 24h into a dated file, so
# a failure that happened at 2am is still visible next week. Cheap: reads only
# the current log window, writes a few KB.
log "collecting error digest"
docker logs --since 24h "$CONTAINER" 2>&1 \
	| grep -Ei '"status":(4[0-9]{2}|5[0-9]{2})|error|panic' \
	| grep -v '"status":404.*favicon' \
	| tail -n 300 > "$BACKUP_DIR/errors-$STAMP.log" || true

ERR_COUNT=$(wc -l < "$BACKUP_DIR/errors-$STAMP.log" | tr -d ' ')
log "error digest: $ERR_COUNT line(s)"
find "$BACKUP_DIR" -maxdepth 1 -name 'errors-*.log' -mtime "+$KEEP_DAYS" -delete

# ── 5. Disk check ─────────────────────────────────────────────────────────────
# A full disk stops SQLite writes, which silently stops lead capture.
USE=$(df -P . | awk 'NR==2 {print $5}' | tr -d '%')
if [ "$USE" -ge 80 ]; then
	log "WARNING: disk ${USE}% full"
fi

# ── 6. Hand ownership to the pull user ────────────────────────────────────────
# Everything above ran as root, so every file here is root-owned and the
# operator cannot read their own backups without sudo — which an unattended
# pull cannot supply. Done last so a failure earlier doesn't leave a
# half-chowned tree.
if [ -n "$BACKUP_OWNER" ]; then
	if chown -R "$BACKUP_OWNER" "$BACKUP_DIR" 2>/dev/null; then
		log "backups owned by $BACKUP_OWNER"
	else
		log "WARNING: could not chown '$BACKUP_DIR' to '$BACKUP_OWNER' — the pull will fail to read these"
	fi
fi

log "maintenance complete"
