# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Where documentation lives

One owner per concern. Put new documentation in the file that already owns the
subject rather than starting a parallel record.

| File | Owns | Tracked? |
|---|---|---|
| `README.md` | What the project is, how it's built, why the non-obvious decisions were made | yes |
| `CLAUDE.md` | This file — orientation for working in the codebase | yes |
| `shed-options.md` | Supplier price list, the source for `pricing.html` | yes |
| Go doc comments | Package and identifier behaviour, in godoc style | yes |
| `docs/BACKLOG.md` | **All** open work, and what was cut and why | no |
| `docs/LOGGING_AND_BACKUPS.md` | What is recorded and retained, and the restore procedure | no |
| `deploy/DEPLOY.md` | Provisioning the VPS from scratch | no |
| `deploy/CD.md` | The deploy loop | no |
| `deploy/NGINX.md` | Rationale for `nginx.conf.example`, and pre-reload verification | no |
| `deploy/OFFSITE.md` | Off-box backup target | no |
| `AUDIT_CHECKLIST.md` | Archived audit history. Superseded — do not add to it | no |

Everything under `deploy/` and `docs/` is gitignored and local to the owner's
machine, because it describes one specific server. Do not add operational
detail — hostnames, paths, credentials, runbook steps — to the tracked files.

## Stack

- **Go + Echo v4** — HTTP server, routing, template rendering
- **html/template** — Go standard library templates (no third-party engine)
- **HTMX 1.9** — self-hosted at `public/js/htmx.min.js`, never a CDN; the CSP allows no external hosts
- **modernc.org/sqlite** — CGO-free SQLite driver; DB file is `prebuilt.db` (git-ignored)
- **godotenv** — loads `.env` at startup; copy `.env.example` → `.env` before running

## Commands

Prefer the `make` targets — CI runs these same targets, so anything that passes
locally passes there.

```bash
make            # list targets
make run        # dev server (default :8080)
make test       # unit tests
make smoke      # 43 end-to-end checks against a real binary, throwaway DB
make ci         # everything CI runs
```

`make smoke` never touches `prebuilt.db` and forces SMTP empty, so it cannot
mail a real address.

## Architecture

The server is a single process with no hot-reload. When editing templates or CSS, restart `go run .` to see changes.

**Top-level split.** These three files are separate on purpose; keep them that way.

1. `main.go` — process lifecycle only: load config, open storage, start, handle signals, shut down gracefully
2. `server.go` — `newServer(cfg)` returns a fully wired Echo instance and starts nothing. All middleware, template mounting, and route registration live here. Tests build the real server through this function, so middleware ordering and body-limit nesting are actually exercised
3. `config.go` — every environment-driven setting, resolved once into a `Config`

**Request flow:**

1. `handlers/` — one file per route group, HTTP concerns only: `home.go`, `contact.go`, `instock.go`, `seo.go` (generated `robots.txt`/`sitemap.xml`), `spam.go` (honeypot + timestamp checks), `errors.go` (the one escaping helper), and the admin trio `admin_items.go`, `admin_photos.go`, `admin_submissions.go`
2. `database/` — schema, migrations, queries; no HTTP awareness. `db.go` opens SQLite, enables WAL, pins the pool to one connection, and creates `contact_submissions`. `inventory.go` owns the inventory tables plus `GenerateCode` (the `{lot}-{styleLetter}-{width}{length}-{sidingCode}{roofCode}` display code, e.g. `1-G-1224-2345` — not unique, the DB id is the real key) and `Describe`
3. `logging.go` — `errorOnlyLogger()`, which logs 4xx/5xx and nothing else. nginx is the system of record for raw traffic
4. `templates/layout.html` — the homepage shell; calls `nav.html`/`footer.html` partials and `{{template "index.html" .}}`, which delegates each section to a partial. `templates/instock.html` is a second full document reusing the same partials. `templates/admin/*.html` are a third template set with their own minimal shell — three separate `ParseGlob` calls in `server.go`
5. `templates/partials/*.html` — one file per section/shared piece, each defining a named template (e.g. `{{define "hero.html"}}`)

**HTMX pattern:** The contact form uses `hx-post="/contact" hx-target="#form-response" hx-swap="outerHTML"`. The handler returns `contact_success.html` on success, or an error fragment built by `errorHTML()` in `handlers/errors.go`. Build error fragments through that helper and nothing else — Echo's `c.HTML` does not auto-escape, and centralising it is what keeps ~26 call sites safe. The spinner uses `.htmx-indicator`, shown by CSS only during `hx-request`.

## Adding content

- **Images** — drop files into `public/images/` and replace `placeholder-img` divs with `<img src="/public/images/yourfile.jpg">`. Each placeholder has a `<!-- REPLACE: ... -->` comment explaining what shot fits.
- **Pricing** — edit `templates/partials/pricing.html`; the pricing card structure is self-contained with a comment block at the top.
- **Phone / service area** — search for `(555) 000-0000` and `[City, State]` across `templates/` to fill in real contact info.

## Environment variables

All resolved once in `config.go`. Unset booleans default to off, and only the
exact string `true` counts as on.

| Variable          | Purpose                                                        |
|-------------------|----------------------------------------------------------------|
| `PORT`            | HTTP listen port (default `8080`)                              |
| `DB_PATH`         | SQLite file location (default `./prebuilt.db`)                 |
| `SMTP_HOST`       | SMTP server hostname                                           |
| `SMTP_PORT`       | SMTP port (default `587`)                                      |
| `SMTP_USER`       | SMTP login / From address                                      |
| `SMTP_PASS`       | SMTP password or app password                                  |
| `CONTACT_EMAIL`   | Where form submissions are emailed                             |
| `ADMIN_USER`      | `/admin` HTTP Basic Auth username                              |
| `ADMIN_PASS`      | `/admin` HTTP Basic Auth password                              |
| `TRUST_PROXY`     | Read the client IP from `X-Forwarded-For`. Required behind nginx |
| `COOKIE_SECURE`   | Mark the admin CSRF cookie `Secure`. Requires HTTPS            |
| `FEATURE_INSTOCK` | Registers the `/instock` route. Off means the page 404s        |
| `SITE_URL`        | Public origin. Gates canonical, Open Graph, and JSON-LD tags   |
| `CSP_REPORT_ONLY` | Report CSP violations instead of blocking                      |

Email is fire-and-forget in a goroutine. If `SMTP_HOST` or `CONTACT_EMAIL` is blank, the email step is silently skipped — submissions still save to SQLite.

If `ADMIN_USER` or `ADMIN_PASS` is blank, `/admin` fails closed (rejects all requests) rather than failing open.

`TRUST_PROXY` and `COOKIE_SECURE` are set in `docker-compose.yml`, not `.env` — both are properties of running behind nginx rather than of a particular deployment.

Feature flags gate **routes**, not just nav links, so a disabled feature 404s rather than sitting unlinked but reachable.

## Admin panel

`/admin` manages inventory (in stock/on hold/sold) from `inventory_items`, protected by HTTP Basic Auth (`handlers/admin_*.go`, `templates/admin/`). The group also carries a rate limiter, a 40 MB body limit, and CSRF — ordered deliberately in `server.go`, so add admin routes to that group rather than registering them standalone. Status changes use htmx (`hx-post` + row swap). Creating and editing items are plain form POSTs (multipart, since both include photo uploads); edit additionally shows a thumbnail grid of existing photos with per-photo htmx delete. Deleting an item removes its DB rows (cascaded, see below) and its entire photo directory. Editing the `siding_colors`/`roof_colors` reference tables has no UI yet — edit them directly via `sqlite3 prebuilt.db`.

**Photo uploads** (`handlers/uploads.go`): each item can have multiple photos tagged `exterior`/`interior`/`feature` (admin-facing only, never shown to customers) via three separate file inputs on the create/edit forms. Files are validated *before* anything is committed — real content-type sniffing (not just the extension), an 8MB-per-file cap, and a 12-photos-per-item cap — so a bad upload never leaves an orphaned item or a half-applied edit. Saved files get a random server-generated name (the client's original filename is never trusted) under `public/images/inventory/{item_id}/`.

Schema: `inventory_images` (id, `inventory_item_id` FK, filename, category, created_at) references `inventory_items.id` with `ON DELETE CASCADE`. SQLite's foreign-key enforcement is per-connection, so `database/db.go` pins the pool to a single connection (`SetMaxOpenConns(1)`) and sets `PRAGMA foreign_keys = ON` at startup — without both, the cascade wouldn't reliably apply. Deleting the DB rows this way only handles the database side; handlers still `os.RemoveAll` the item's photo directory separately since SQLite cascades don't touch the filesystem.

`inventory_items` originally had a single `image_filename` column; `database/inventory.go`'s `migrateLegacyImageFilename()` is a one-time, idempotent migration (guarded by checking whether the column still exists) that copies any legacy value into `inventory_images` and drops the column. It only matters for databases created before the photo-gallery feature existed.

## In-stock page

`/instock` shows already-built sheds sitting on one of three physical lots, grouped by lot within In Stock / On Hold / Sold tabs (reuses the `.pricing-tab` JS/CSS pattern from the Pricing section). Sold items stay visible with a SOLD ribbon rather than disappearing, as proof inventory actually moves. Each shed's photos (if more than one) render as a carousel — the same `[data-carousel]`/`.carousel-slide` pattern used by the Styles section carousels, reused verbatim with zero extra JS. The human-readable inventory code (e.g. `1-G-1224-2345`) is internal-only and never rendered on this page — customers see plain dimensions/style/lot instead, and the "Ask About This One" button opens a modal that emails the full code + description to the business, not the customer.
