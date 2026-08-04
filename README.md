# Prebuilt Sheds LLC

Marketing site and inventory system for a shed builder. Server-rendered Go,
SQLite, and htmx — one static binary, no frontend build step, no runtime
dependencies beyond the container.

---

## Stack

| | |
|---|---|
| Runtime | Go 1.25+, Echo v4 |
| Views | `html/template`, htmx 1.9 (self-hosted) |
| Storage | SQLite via `modernc.org/sqlite` (pure Go, no cgo) |
| Deploy | Docker, nginx + certbot on the host |

Four direct dependencies. No ORM, no JS toolchain, no CSS framework.

---

## Architecture

```
main.go          process lifecycle: config, storage, signals, shutdown
server.go        the wired Echo instance: middleware, templates, routes
config.go        every environment-driven setting, resolved once
handlers/        one file per route group; HTTP concerns only
database/        schema, migrations, queries; no HTTP awareness
templates/       layout + partials; admin has its own template set
public/          static assets, uploaded photos
```

Requests are server-rendered end to end. htmx handles the two interactions that
benefit from partial updates — contact submission and admin status changes — by
swapping HTML fragments returned from the same handlers. No client-side state,
no JSON API, no hydration.

**Routes.** Public: `/`, `/contact`, `/instock`. Admin: `/admin/*` behind auth,
CSRF, and a separate body limit. `robots.txt` and `sitemap.xml` are served from
the root, not `/public`.

---

## Engineering notes

Decisions that aren't obvious from reading the code:

**Pure-Go SQLite.** `modernc.org/sqlite` compiles without cgo, so
`CGO_ENABLED=0` produces a static binary and the runtime image needs no libc
matching. Trades some raw speed for a materially simpler build and image.

**Connection pool pinned to one connection.** SQLite's `PRAGMA foreign_keys` is
per-connection, and `database/sql` pools transparently. Without
`SetMaxOpenConns(1)`, the `ON DELETE CASCADE` on inventory photos would apply
only on whichever connection happened to run the pragma. SQLite serializes
writes regardless, so at this scale the pool bought nothing.

**Money is `int64` cents, rounded not truncated.** `int64(dollars * 100)`
silently loses a cent when float multiplication lands just under the integer
(`19.99 * 100` → `1998.9999999999998`). Uses `math.Round`, with a regression
test.

**Uploads validate before anything commits.** Size, count, and real content
type (sniffed from bytes, not the extension) are all checked before the item
row is created or a file is written, so a rejected upload can't leave an
orphaned record or a half-applied edit. Stored filenames are server-generated;
the client's filename is never trusted or echoed back.

**The server is built by a function, not by `main()`.** `newServer(cfg)` returns
a fully wired instance and starts nothing, so tests exercise the real middleware
chain rather than a reconstruction of it. Middleware ordering, a group's
auto-registered `RouteNotFound`, and nested body limits are properties of that
function alone — asserting them against a hand-built `echo.New()` would pass
through exactly the regressions worth catching.

**Escaping lives in one function.** Error fragments render through a single
helper that escapes unconditionally, rather than relying on ~26 call sites to
remember. Echo's `c.HTML` does not auto-escape.

**Feature flags gate routes, not just links.** A disabled feature's routes are
never registered, so the page 404s instead of sitting unlinked but reachable.
Exposed to templates as a function so shared partials need no data plumbing.

**Schema changes follow SQLite's table-rebuild procedure.** No
`ALTER TABLE ADD CONSTRAINT` exists, so adding CHECK constraints rebuilds the
table inside a transaction and verifies referential integrity afterward.
Migrations are idempotent and guarded — safe to run on every boot.

---

## Security

- CSRF on all admin mutations, covering both form posts and htmx requests;
  cookie explicitly scoped to `/admin`, `SameSite=Strict`, `Secure` in production
- Per-IP rate limiting on public form endpoints
- `nosniff`, `X-Frame-Options`, and HSTS (set explicitly — Echo's default
  `HSTSMaxAge` is `0`, which silently disables it)
- Body limits scoped per route group; uploads capped by size, count, and type
- Auth fails closed when misconfigured; constant-time credential comparison
- Server timeouts and graceful shutdown
- Container runs as a non-root user; the app binds to loopback only

---

## Local development

```bash
cp .env.example .env
go run .                      # :8080
```

```bash
make            # list targets
make test       # unit tests
make smoke      # 43 end-to-end checks against a real binary
make ci         # everything CI runs, ~13s
```

Templates and CSS are read from disk at startup; restart to pick up changes.

`make smoke` exercises routing, auth, CSRF via both token sources, security
headers, body limits, rate limiting, and the form spam defences against a real
running binary. Output is deterministic, so capturing it before and after a
dependency change reduces the review to a `diff`. It runs against a temporary
database with SMTP forced empty and never touches `prebuilt.db`.

CI calls these same `make` targets rather than restating commands in YAML, so
the pipeline is reproducible locally and cannot drift from what developers run.
`make check-docker-go` asserts the Dockerfile's builder image satisfies the `go`
directive — a dependency upgrade that raises it otherwise breaks the deploy
while passing every local check.

## Configuration

Set via environment or `.env`. Unset flags default to off.

| Variable | Purpose |
|---|---|
| `PORT` | Listen port (default `8080`) |
| `DB_PATH` | SQLite file location (default `./prebuilt.db`) |
| `SMTP_*`, `CONTACT_EMAIL` | Contact form delivery; skipped if unset |
| `ADMIN_USER`, `ADMIN_PASS` | Admin credentials |
| `TRUST_PROXY` | Read client IP from `X-Forwarded-For`. Required behind nginx |
| `COOKIE_SECURE` | Mark cookies `Secure`. Requires HTTPS |
| `FEATURE_INSTOCK` | Enables the public inventory page |
| `SITE_URL` | Public origin. Enables canonical and Open Graph tags |
| `CSP_REPORT_ONLY` | Report CSP violations instead of blocking. Use on first deploy after a policy change |

## Deployment

CI builds and publishes the image to GHCR behind a `needs:` gate, so an image
that failed tests cannot reach the registry. The server pulls that image rather
than compiling:

```bash
docker compose pull
docker compose up -d --no-build
```

`--no-build` is not optional there. `docker-compose.yml` sets both `build:` and
`image:`, so a plain `up -d` with no cached image silently starts compiling
instead — and `modernc.org/sqlite` is a single 8.6 MB generated source file that
takes minutes on one vCPU. Building locally is the other direction:
`docker compose up -d --build`.

Every image is also tagged `sha-<short>`, so a rollback is
`IMAGE_TAG=sha-1a2b3c4 docker compose up -d`. `latest` is a moving tag and
cannot be rolled back to.

nginx and certbot run on the host and terminate TLS. The app runs in Docker and
binds to loopback only — Docker writes its own iptables rules that bypass the
host firewall, so publishing to `0.0.0.0` would expose the app directly no
matter what ufw says.

The database and uploads use named volumes rather than bind mounts. Docker seeds
a named volume with the ownership baked into the image, so the container's
non-root user can write without any host-side `chown`. `restart: unless-stopped`
covers crashes and reboots; container logs are size-capped so they cannot fill
the disk and stall SQLite writes.

Two settings are mandatory behind the proxy and are set in compose:
`TRUST_PROXY`, so per-IP rate limiting sees the real client rather than nginx,
and `COOKIE_SECURE`. The proxy also needs `client_max_body_size` raised to at
least the app's upload limit — nginx defaults to 1 MB and otherwise rejects
photo uploads with a bare 413 before the request reaches a handler.

Provisioning, DNS, certificate issuance, the nginx server block, and backup
policy live in operational runbooks maintained outside this repository, since
they describe one specific server rather than the software.

---

## Status

The marketing site and contact flow are production-ready. The inventory page is
built and feature-flagged off pending real inventory data.

The admin panel uses HTTP Basic Auth. Session auth was specified and then
deliberately not built: over HTTPS, with a long password, per-IP rate limiting,
CSRF, and fail2ban, the only thing it adds here is a logout button for a
single-operator panel. Revisit if a second person ever needs access.

Nightly backups are scripted — a consistent `sqlite3 .backup`, an incremental
photo mirror, and pruning. The off-box copy is not yet enabled, so every backup
currently sits on the same disk as the data it protects. That is the largest
remaining operational gap.
