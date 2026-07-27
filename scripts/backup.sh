#!/bin/sh
# DEPRECATED — superseded by scripts/maintenance.sh
#
# This script targeted the pre-Docker layout (./prebuilt.db and
# public/images/inventory/). Both now live in named Docker volumes, so on the
# deployed server it aborts with "prebuilt.db not found" and archives nothing.
# It fails loudly rather than quietly, but from cron that error lands in a log
# nobody reads — so the practical result is still no backups.
#
# Kept as a failing stub rather than deleted, in case an old crontab entry
# still points at this path.

echo "backup.sh is deprecated and does not work against the Docker deployment." >&2
echo "Use scripts/maintenance.sh instead: database backup, incremental photo" >&2
echo "mirror, retention pruning, and an error digest." >&2
echo "" >&2
echo "Crontab entry:" >&2
echo "  15 3 * * * cd /srv/prebuilt && nice -n 19 ionice -c3 ./scripts/maintenance.sh >> backups/maintenance.log 2>&1" >&2
exit 1
