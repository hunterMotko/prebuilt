#!/usr/bin/env bash
#
# Deploy smoke test — the checks that can be proven without a browser.
#
# Written as a baseline/after diff tool for framework upgrades, but it is worth
# running before any deploy: it is what would have caught the static-asset 404
# that the e.Group("/public", mw) change caused, immediately rather than in
# production.
#
#   ./scripts/admin-smoke.sh > baseline.txt    # before the change
#   ./scripts/admin-smoke.sh > after.txt       # after
#   diff baseline.txt after.txt                # anything at all is a regression
#
# Output is deliberately deterministic — no timestamps, no CSRF tokens, no
# durations — so a clean diff means nothing moved.
#
# SAFETY: runs against a throwaway database in a temp directory and forces the
# SMTP variables empty, so it can never write to the real prebuilt.db or send
# mail to a real customer. Do not "fix" that by letting it inherit .env.
#
# What it does NOT cover, because curl cannot: htmx swapping a rendered row in
# place, the delegated data-autosubmit listener surviving that swap, photo
# upload and deletion, and CSP violations. Those are the manual browser pass.

# Overridable because any other dev server on the same port makes every check
# interrogate the wrong process — the failures look like app bugs, not a
# port collision. The default is deliberately obscure.
PORT="${SMOKE_PORT:-8471}"
BASE="http://127.0.0.1:${PORT}"
USER="smoke"
PASS="smoketest"

WORK="$(mktemp -d)"
BIN="${WORK}/prebuilt"
JAR="${WORK}/cookies"
SRV_LOG="${WORK}/server.log"
DB="${WORK}/smoke.db"

fails=0
srv_pid=""

cleanup() {
	[ -n "$srv_pid" ] && kill "$srv_pid" 2>/dev/null
	wait "$srv_pid" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

start_server() {
	[ -n "$srv_pid" ] && { kill "$srv_pid" 2>/dev/null; wait "$srv_pid" 2>/dev/null; }
	rm -f "$JAR"
	# Every SMTP variable is blanked explicitly. godotenv does not override
	# variables already present in the environment, so setting them here — even
	# to the empty string — is what stops .env from being picked up and a test
	# submission being mailed to a real inbox.
	env SMTP_HOST= SMTP_USER= SMTP_PASS= CONTACT_EMAIL= \
		DB_PATH="$DB" PORT="$PORT" \
		ADMIN_USER="$USER" ADMIN_PASS="$PASS" \
		FEATURE_INSTOCK=true \
		"$BIN" >"$SRV_LOG" 2>&1 &
	srv_pid=$!

	for _ in $(seq 1 50); do
		curl -fsS -o /dev/null "${BASE}/" 2>/dev/null && return 0
		sleep 0.1
	done
	echo "FATAL server did not come up; log follows" >&2
	cat "$SRV_LOG" >&2
	exit 1
}

# check <name> <expected> <actual>
check() {
	if [ "$2" = "$3" ]; then
		printf 'ok    %-46s %s\n' "$1" "$3"
	else
		printf 'FAIL  %-46s want=%s got=%s\n' "$1" "$2" "$3"
		fails=$((fails + 1))
	fi
}

# status <curl args...> -> HTTP status code
status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

# Every read of the database goes through here, never bare `sqlite3`.
#
# busy_timeout is per connection, and the CLI is a different process from the
# server, so it does NOT inherit the app's 5s — it gets SQLite's default of 0,
# which means "fail immediately on any lock". POST /contact returns as soon as
# the row is saved and then writes the email status from a goroutine, so a query
# issued on the next line lands inside that write and died with "database is
# locked". Intermittent by nature: it passed locally and on the PR, and only
# went red on main.
#
# The app now also runs in WAL mode, under which these reads would not block at
# all. This stays because it is the correct way to invoke the CLI regardless,
# and it still covers the cases WAL does not — a write, or a checkpoint.
sq() { sqlite3 -cmd '.timeout 5000' "$@"; }

# Checked up front so a missing tool reports itself rather than surfacing as a
# wall of failed assertions with empty "got=" values, which is how this looks
# on a CI runner that happens not to ship sqlite3.
for tool in go curl sqlite3; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "FATAL required tool not found: $tool" >&2
		exit 1
	}
done

echo "=== build ==="
if ! go build -o "$BIN" . 2>&1; then
	echo "FATAL build failed" >&2
	exit 1
fi
check "build" "ok" "ok"
printf 'echo  %-46s %s\n' "version" "$(go list -m github.com/labstack/echo/v4 | awk '{print $2}')"

start_server

echo
echo "=== public routes ==="
check "GET /"                       200 "$(status "${BASE}/")"
check "HEAD /"                      200 "$(status -I "${BASE}/")"
check "GET /instock"                200 "$(status "${BASE}/instock")"
check "GET /robots.txt"             200 "$(status "${BASE}/robots.txt")"
check "GET /sitemap.xml"            200 "$(status "${BASE}/sitemap.xml")"

# Seam 3: attaching middleware to a group makes Echo auto-register a catch-all
# RouteNotFound for that prefix, which once shadowed the static route and 404'd
# every asset on the site.
echo
echo "=== static assets (seam 3) ==="
check "GET /public/css/style.css"   200 "$(status "${BASE}/public/css/style.css")"
check "GET /public/js/main.js"      200 "$(status "${BASE}/public/js/main.js")"
check "GET /public/js/admin.js"     200 "$(status "${BASE}/public/js/admin.js")"
check "GET /public/js/htmx.min.js"  200 "$(status "${BASE}/public/js/htmx.min.js")"

echo
echo "=== error handling (seam 10) ==="
check "GET /nope -> 404"            404 "$(status "${BASE}/nope")"
# Echo's default handler emits JSON; ours renders error.html. A regression here
# means a mistyped URL shows raw JSON on a marketing site.
notfound_ct="$(curl -s -o /dev/null -w '%{content_type}' "${BASE}/nope" | cut -d';' -f1)"
check "404 content-type"            "text/html" "$notfound_ct"

echo
echo "=== response headers (seam 7) ==="
hdrs="$(curl -s -D - -o /dev/null "${BASE}/")"
has_hdr() { echo "$hdrs" | grep -qi "^$1:" && echo present || echo MISSING; }
check "Content-Security-Policy"     present "$(has_hdr 'Content-Security-Policy')"
check "Referrer-Policy"             present "$(has_hdr 'Referrer-Policy')"
check "Permissions-Policy"          present "$(has_hdr 'Permissions-Policy')"
check "X-Content-Type-Options"      present "$(has_hdr 'X-Content-Type-Options')"
check "X-Frame-Options"             present "$(has_hdr 'X-Frame-Options')"
check "X-XSS-Protection value"      "0" "$(echo "$hdrs" | grep -i '^X-XSS-Protection:' | tr -d '\r' | awk '{print $2}')"
# Only emitted over TLS or behind X-Forwarded-Proto: https. Dormant locally by
# design, so its absence here is correct and its presence would be the bug.
check "HSTS absent on plain HTTP"   MISSING "$(has_hdr 'Strict-Transport-Security')"
check "HSTS present w/ XFP header"  present \
	"$(curl -s -D - -o /dev/null -H 'X-Forwarded-Proto: https' "${BASE}/" \
		| grep -qi '^Strict-Transport-Security:' && echo present || echo MISSING)"

echo
echo "=== admin auth (seams 1, 5) ==="
check "GET /admin no creds"         401 "$(status "${BASE}/admin")"
check "GET /admin bad creds"        401 "$(status -u wrong:wrong "${BASE}/admin")"
# Seam 1: admin.GET("") must resolve to exactly /admin. If group path joining
# changes, this 404s while every sub-route keeps working.
check "GET /admin good creds"       200 "$(status -u "$USER:$PASS" "${BASE}/admin")"
check "GET /admin/submissions"      200 "$(status -u "$USER:$PASS" "${BASE}/admin/submissions")"
check "GET /admin/inventory/new"    200 "$(status -u "$USER:$PASS" "${BASE}/admin/inventory/new")"

echo
echo "=== CSRF (seam 2) ==="
# The form path and the header path are looked up from ONE comma-separated
# TokenLookup string. They are tested separately because a parsing change can
# break one while the other keeps working — htmx would keep working while
# create/edit silently started rejecting, which reads as a template bug.
check "POST /admin/inventory no token" 400 \
	"$(status -u "$USER:$PASS" -X POST "${BASE}/admin/inventory" -d 'lot=1')"

# Cookie jar + token scraped from the real form, exactly as a browser would.
form_html="$(curl -s -u "$USER:$PASS" -c "$JAR" "${BASE}/admin/inventory/new")"
token="$(echo "$form_html" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
check "csrf token in form"          present "$([ -n "$token" ] && echo present || echo MISSING)"

create_status="$(status -u "$USER:$PASS" -b "$JAR" -c "$JAR" -X POST "${BASE}/admin/inventory" \
	-F "csrf=${token}" -F 'lot=1' -F 'style=Gable' -F 'width=12' -F 'length=24' \
	-F 'siding_code=30' -F 'roof_code=20' -F 'price=4999' -F 'notes=smoke test')"
check "POST /admin/inventory (form token)" 303 "$create_status"
check "item persisted"              1 "$(sq "$DB" 'SELECT COUNT(*) FROM inventory_items;')"

item_id="$(sq "$DB" 'SELECT id FROM inventory_items LIMIT 1;')"
check "GET /admin/inventory/:id/edit" 200 \
	"$(status -u "$USER:$PASS" "${BASE}/admin/inventory/${item_id}/edit")"

# Header path — what htmx uses for status flips and photo deletes.
check "POST status (header token)"  200 \
	"$(status -u "$USER:$PASS" -b "$JAR" -c "$JAR" -X POST \
		-H "X-CSRF-Token: ${token}" \
		"${BASE}/admin/inventory/${item_id}/status" -d 'status=sold')"
check "status change persisted"     sold \
	"$(sq "$DB" "SELECT status FROM inventory_items WHERE id=${item_id};")"
check "POST bogus status rejected"  422 \
	"$(status -u "$USER:$PASS" -b "$JAR" -c "$JAR" -X POST \
		-H "X-CSRF-Token: ${token}" \
		"${BASE}/admin/inventory/${item_id}/status" -d 'status=bogus')"

echo
echo "=== public form + spam defence ==="
old_ts=$(( $(date +%s) - 60 ))
form='name=Smoke&phone=5550000&email=smoke@example.com&style=Gable&size=10x12'
check "POST /contact valid"         200 "$(status -X POST "${BASE}/contact" -d "${form}&form_ts=${old_ts}")"
check "POST /contact missing field" 422 "$(status -X POST "${BASE}/contact" -d 'name=OnlyAName')"
# Both spam paths must return the ordinary success page, not an error — a bot
# that can tell it was filtered is a bot that adapts.
check "POST /contact honeypot"      200 "$(status -X POST "${BASE}/contact" -d "${form}&form_ts=${old_ts}&contact_ref=spam")"
check "POST /contact instant submit" 200 "$(status -X POST "${BASE}/contact" -d "${form}&form_ts=$(date +%s)")"
check "only the real lead saved"    1 "$(sq "$DB" 'SELECT COUNT(*) FROM contact_submissions;')"
check "spam rejections logged"      2 "$(grep -c spam_rejected "$SRV_LOG")"

echo
echo "=== submission lead status + delete ==="
# Rides the one real lead saved above; jar + token from the CSRF section are
# still valid on this server instance.
sub_id="$(sq "$DB" 'SELECT id FROM contact_submissions LIMIT 1;')"
check "lead status defaults to new" new \
	"$(sq "$DB" "SELECT lead_status FROM contact_submissions WHERE id=${sub_id};")"
check "POST lead status"            200 \
	"$(status -u "$USER:$PASS" -b "$JAR" -c "$JAR" -X POST \
		-H "X-CSRF-Token: ${token}" \
		"${BASE}/admin/submissions/${sub_id}/status" -d 'status=confirmed')"
check "lead status persisted"       confirmed \
	"$(sq "$DB" "SELECT lead_status FROM contact_submissions WHERE id=${sub_id};")"
check "POST bogus lead rejected"    422 \
	"$(status -u "$USER:$PASS" -b "$JAR" -c "$JAR" -X POST \
		-H "X-CSRF-Token: ${token}" \
		"${BASE}/admin/submissions/${sub_id}/status" -d 'status=bogus')"
check "DELETE submission"           200 \
	"$(status -u "$USER:$PASS" -b "$JAR" -c "$JAR" -X DELETE \
		-H "X-CSRF-Token: ${token}" \
		"${BASE}/admin/submissions/${sub_id}")"
check "submission deleted"          0 "$(sq "$DB" 'SELECT COUNT(*) FROM contact_submissions;')"
check "DELETE missing -> 404"       404 \
	"$(status -u "$USER:$PASS" -b "$JAR" -c "$JAR" -X DELETE \
		-H "X-CSRF-Token: ${token}" \
		"${BASE}/admin/submissions/${sub_id}")"

echo
echo "=== body limit (seam 4) ==="
# Public POSTs are capped at 3M; the admin group at 40M. Nesting matters:
# BodyLimit wraps the body reader, so a stricter outer wrap cannot be loosened
# by a looser inner one, which is why the 3M limit is scoped to public routes
# rather than applied globally.
big="${WORK}/big.bin"
head -c 4000000 /dev/zero | tr '\0' 'x' > "$big"
check "public POST >3M rejected"    413 "$(status -X POST "${BASE}/contact" --data-binary "@${big}")"
check "admin POST >3M accepted"     400 \
	"$(status -u "$USER:$PASS" -X POST "${BASE}/admin/inventory" --data-binary "@${big}")"

# Fresh server: the checks above have already consumed part of the admin
# limiter's burst, so this would otherwise report a different number every run.
echo
echo "=== rate limiting (seam 9) ==="
start_server
codes=""
for _ in $(seq 1 25); do
	codes="${codes}$(status -u wrong:wrong "${BASE}/admin") "
done
check "admin: 401s before throttle" 20 "$(echo "$codes" | tr ' ' '\n' | grep -c '^401$')"
check "admin: 429s after burst"      5 "$(echo "$codes" | tr ' ' '\n' | grep -c '^429$')"

echo
if [ "$fails" -eq 0 ]; then
	echo "RESULT: all checks passed"
else
	echo "RESULT: ${fails} check(s) failed"
fi
exit "$fails"
