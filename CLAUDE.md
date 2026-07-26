# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Stack

- **Go + Echo v4** — HTTP server, routing, template rendering
- **html/template** — Go standard library templates (no third-party engine)
- **HTMX 1.9** — loaded from CDN; powers the contact form submission without a full page reload
- **modernc.org/sqlite** — CGO-free SQLite driver; DB file is `prebuilt.db` (git-ignored)
- **godotenv** — loads `.env` at startup; copy `.env.example` → `.env` before running

## Commands

```bash
go run .           # start dev server (default :8080)
go build .         # compile binary
go test ./...      # run all tests
PORT=3000 go run . # run on a custom port
```

## Architecture

The server is a single process with no hot-reload. When editing templates or CSS, restart `go run .` to see changes.

**Request flow:**
1. `main.go` — registers routes and mounts `TemplateRenderer` (wraps `html/template`); also wires the `/admin` route group behind HTTP Basic Auth
2. `handlers/` — one file per route group; `home.go` renders the homepage, `contact.go` handles `POST /contact`, `instock.go` renders the public `/instock` page, `admin.go` handles the `/admin` inventory management routes
3. `templates/layout.html` — the outer shell for the homepage (`<html>`, calls `nav.html`/`footer.html` partials); calls `{{template "index.html" .}}`; `index.html` delegates each section to a partial. `templates/instock.html` is a second full-document page that also reuses `nav.html`/`footer.html`. `templates/admin/*.html` are a third, separate template set with their own minimal shell (no nav/footer) — loaded via a third `ParseGlob` in `main.go`
4. `templates/partials/*.html` — one file per section/shared piece; each defines a named template (e.g. `{{define "hero.html"}}`, `{{define "nav.html"}}`)
5. `database/db.go` — opens SQLite, creates `contact_submissions` table on startup, exposes `SaveContactSubmission`
6. `database/inventory.go` — creates `inventory_items`, `siding_colors`, `roof_colors` tables (with seeded placeholder color codes), exposes inventory CRUD/list functions, `GenerateCode` (builds the `{lot}-{styleLetter}-{width}{length}-{sidingCode}{roofCode}` display code, e.g. `1-G-1224-2345` — not unique, the DB id is the real key), and `Describe` (full human-readable breakdown for reuse anywhere the raw code isn't enough)

**HTMX pattern:** The contact form uses `hx-post="/contact" hx-target="#form-response" hx-swap="outerHTML"`. The handler returns either `contact_success.html` (success) or an inline error HTML string. The spinner uses the `.htmx-indicator` class — CSS shows it only during `hx-request`.

## Adding content

- **Images** — drop files into `public/images/` and replace `placeholder-img` divs with `<img src="/public/images/yourfile.jpg">`. Each placeholder has a `<!-- REPLACE: ... -->` comment explaining what shot fits.
- **Pricing** — edit `templates/partials/pricing.html`; the pricing card structure is self-contained with a comment block at the top.
- **Phone / service area** — search for `(555) 000-0000` and `[City, State]` across `templates/` to fill in real contact info.

## Environment variables

| Variable        | Purpose                                      |
|-----------------|----------------------------------------------|
| `PORT`          | HTTP listen port (default `8080`)            |
| `SMTP_HOST`     | SMTP server hostname                         |
| `SMTP_PORT`     | SMTP port (default `587`)                    |
| `SMTP_USER`     | SMTP login / From address                    |
| `SMTP_PASS`     | SMTP password or app password                |
| `CONTACT_EMAIL` | Where form submissions are emailed           |
| `ADMIN_USER`    | `/admin` HTTP Basic Auth username             |
| `ADMIN_PASS`    | `/admin` HTTP Basic Auth password             |

Email is fire-and-forget in a goroutine. If `SMTP_HOST` or `CONTACT_EMAIL` is blank, the email step is silently skipped — submissions still save to SQLite.

If `ADMIN_USER` or `ADMIN_PASS` is blank, `/admin` fails closed (rejects all requests) rather than failing open.

## Admin panel

`/admin` manages inventory (in stock/on hold/sold) from `inventory_items`, protected by HTTP Basic Auth (`handlers/admin.go`, `templates/admin/`). Status changes use htmx (`hx-post` + row swap). Creating and editing items are plain form POSTs (multipart, since both include photo uploads); edit additionally shows a thumbnail grid of existing photos with per-photo htmx delete. Deleting an item removes its DB rows (cascaded, see below) and its entire photo directory. Editing the `siding_colors`/`roof_colors` reference tables has no UI yet — edit them directly via `sqlite3 prebuilt.db`.

**Photo uploads** (`handlers/uploads.go`): each item can have multiple photos tagged `exterior`/`interior`/`feature` (admin-facing only, never shown to customers) via three separate file inputs on the create/edit forms. Files are validated *before* anything is committed — real content-type sniffing (not just the extension), an 8MB-per-file cap, and a 12-photos-per-item cap — so a bad upload never leaves an orphaned item or a half-applied edit. Saved files get a random server-generated name (the client's original filename is never trusted) under `public/images/inventory/{item_id}/`.

Schema: `inventory_images` (id, `inventory_item_id` FK, filename, category, created_at) references `inventory_items.id` with `ON DELETE CASCADE`. SQLite's foreign-key enforcement is per-connection, so `database/db.go` pins the pool to a single connection (`SetMaxOpenConns(1)`) and sets `PRAGMA foreign_keys = ON` at startup — without both, the cascade wouldn't reliably apply. Deleting the DB rows this way only handles the database side; handlers still `os.RemoveAll` the item's photo directory separately since SQLite cascades don't touch the filesystem.

`inventory_items` originally had a single `image_filename` column; `database/inventory.go`'s `migrateLegacyImageFilename()` is a one-time, idempotent migration (guarded by checking whether the column still exists) that copies any legacy value into `inventory_images` and drops the column. It only matters for databases created before the photo-gallery feature existed.

## In-stock page

`/instock` shows already-built sheds sitting on one of three physical lots, grouped by lot within In Stock / On Hold / Sold tabs (reuses the `.pricing-tab` JS/CSS pattern from the Pricing section). Sold items stay visible with a SOLD ribbon rather than disappearing, as proof inventory actually moves. Each shed's photos (if more than one) render as a carousel — the same `[data-carousel]`/`.carousel-slide` pattern used by the Styles section carousels, reused verbatim with zero extra JS. The human-readable inventory code (e.g. `1-G-1224-2345`) is internal-only and never rendered on this page — customers see plain dimensions/style/lot instead, and the "Ask About This One" button opens a modal that emails the full code + description to the business, not the customer.
