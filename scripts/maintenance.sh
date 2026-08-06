#!/bin/sh
# Nightly maintenance: consistent database backup, incremental photo mirror,
# retention pruning, and an error digest.
#
# Replaces the old scripts/backup.sh, which still targeted the pre-Docker
# layout and therefore backed up nothing while appearing to succeed.
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
# Install: the invoking user must be able to reach the docker daemon. If the
# deploy user is in the docker group, use its crontab; on boxes where that
# membership is deliberately withheld (docker-socket access is root-equivalent),
# use root's instead: `sudo crontab -e`. Absolute paths on both sides — the
# script self-locates, so the path only has to find it, but a relative log path
# under cron fails silently:
#   15 3 * * * nice -n 19 ionice -c3 /path/to/repo/scripts/maintenance.sh >> /path/to/repo/backups/maintenance.log 2>&1
#
# nice/ionice keep this off the critical path: even under load it yields CPU
# and disk to request handling rather than competing with it.

set -eu

cd "$(dirname "$0")/.."

BACKUP_DIR=${BACKUP_DIR:-backups}
KEEP_DAYS=${KEEP_DAYS:-14}
CONTAINER=${CONTAINER:-prebuilt-sheds}
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
log "backing up database"
docker exec "$CONTAINER" sqlite3 /data/prebuilt.db ".backup '/tmp/snapshot.db'"
docker cp "$CONTAINER:/tmp/snapshot.db" "$BACKUP_DIR/db/prebuilt-$STAMP.db"
docker exec "$CONTAINER" rm -f /tmp/snapshot.db
gzip -9 -f "$BACKUP_DIR/db/prebuilt-$STAMP.db"
log "database backed up ($(du -h "$BACKUP_DIR/db/prebuilt-$STAMP.db.gz" | cut -f1))"

# ── 2. Photos ─────────────────────────────────────────────────────────────────
# The volume name is read from the running container rather than hardcoded,
# because Compose prefixes it with the project directory name.
UPLOAD_VOL=$(docker inspect -f \
	'{{range .Mounts}}{{if eq .Destination "/app/public/images/inventory"}}{{.Name}}{{end}}{{end}}' \
	"$CONTAINER")

if [ -n "$UPLOAD_VOL" ]; then
	log "mirroring photos from volume $UPLOAD_VOL"
	docker run --rm \
		-v "$UPLOAD_VOL":/src:ro \
		-v "$(pwd)/$BACKUP_DIR/uploads":/dst \
		alpine:3.20 sh -c 'cp -a -n /src/. /dst/ 2>/dev/null || true'
	log "photo mirror now $(du -sh "$BACKUP_DIR/uploads" | cut -f1)"
else
	log "WARNING: no uploads volume found on $CONTAINER — skipping photos"
fi

# ── 3. Retention ──────────────────────────────────────────────────────────────
# Each database snapshot is a full, self-contained copy, so deleting old ones
# never affects the ability to restore from a newer one. The photo mirror is
# deliberately NOT pruned: a photo deleted in the admin panel by mistake stays
# recoverable here.
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

log "maintenance complete"

# ── 6. Off-box copy ───────────────────────────────────────────────────────────
# Everything above still lives on the same disk as the thing it backs up, which
# does not survive that disk failing. Uncomment one of these after the one-time
# setup it needs. rclone config opens a browser to authorise the account, so it
# has to be run interactively once.
#
#   rclone sync "$BACKUP_DIR" remote:prebuilt-sheds-backups/ --transfers 2
#   scp -r "$BACKUP_DIR" user@backup-host:/path/to/backups/
#
# --transfers 2 keeps rclone from saturating a small VPS's uplink.
