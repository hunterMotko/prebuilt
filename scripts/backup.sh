#!/bin/sh
# Backs up prebuilt.db + uploaded shed photos into a timestamped archive.
#
# Hosting-agnostic on purpose: this only writes into ./backups/ on whatever
# machine runs it. Where hosting ends up (VPS, home server, etc.) determines
# how those archives should also get copied OFF that machine — see the
# "off-box copy" section below once that's decided. A backup that only lives
# on the same disk as the thing it's backing up doesn't protect against that
# disk failing.
#
# Usage: ./scripts/backup.sh
# Typically run daily via cron — see the crontab line at the bottom of this
# file for the exact entry to add.

set -eu

cd "$(dirname "$0")/.."

BACKUP_DIR="backups"
STAMP="$(date +%Y-%m-%d-%H%M%S)"
ARCHIVE="$BACKUP_DIR/prebuilt-$STAMP.tar.gz"
KEEP_DAYS=14

mkdir -p "$BACKUP_DIR"

if [ ! -f prebuilt.db ]; then
	echo "backup.sh: prebuilt.db not found in $(pwd) — nothing to back up" >&2
	exit 1
fi

# --images if the directory doesn't exist yet (e.g. no photos uploaded yet)
IMAGES_ARG=""
if [ -d public/images/inventory ]; then
	IMAGES_ARG="public/images/inventory"
fi

tar -czf "$ARCHIVE" prebuilt.db $IMAGES_ARG

echo "backup.sh: wrote $ARCHIVE ($(du -h "$ARCHIVE" | cut -f1))"

# Rotation: delete archives older than KEEP_DAYS so backups/ doesn't grow
# forever. Every archive is self-contained (a full copy, not incremental),
# so deleting an old one never affects the ability to restore from a newer
# one.
find "$BACKUP_DIR" -name 'prebuilt-*.tar.gz' -mtime "+$KEEP_DAYS" -delete

# --- Off-box copy (add once hosting + a cloud target are decided) ---
# Anything below this line is optional and commented out until then. Once
# you have hosting sorted, uncomment ONE of these (after the one-time setup
# each requires):
#
#   rclone (Google Drive / Dropbox / S3 / B2 — run `rclone config` once
#   first, which opens a browser to authorize your account):
#     rclone copy "$ARCHIVE" remote:prebuilt-sheds-backups/
#
#   Plain scp to another machine you control:
#     scp "$ARCHIVE" user@backup-host:/path/to/backups/

# --- Crontab entry (run once: `crontab -e`, then paste this line) ---
# 0 3 * * * /full/path/to/prebuilt/scripts/backup.sh >> /full/path/to/prebuilt/backups/backup.log 2>&1
