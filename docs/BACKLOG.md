# Development Backlog — Prebuilt Sheds LLC

Full-team review of the live site: defects, security, operations, performance,
SEO, accessibility, product gaps, and technical debt.

Written 2026-07-26, against the deployed codebase. Supersedes the open items in
`AUDIT_CHECKLIST.md`, which stays as the historical audit record.

**Conventions**

- **Sev** — `S1` outage/data-loss/exploitable · `S2` degrades trust or loses
  business · `S3` quality/maintainability · `S4` nice-to-have
- **Effort** — `S` under 2h · `M` half a day · `L` 1–3 days · `XL` a week+
- Items marked *verified* were reproduced or confirmed against the source.
  Items marked *inferred* are reasoned from the code but not yet reproduced.

---

## 0. The lead-loss chain — 2 of 3 links closed

Three individually-minor gaps combined into the worst realistic failure on this
site, and none of them would have announced itself.

| # | Link | Status |
|---|---|---|
| 1 | Email errors discarded (`_ = smtp.SendMail`) | **Closed** |
| 2 | `contact_submissions` unreadable — no admin screen | **Closed** |
| 3 | No backups | **Open** |
**Link 3 — open.** Deferred deliberately; see `OPS-1`.

### Residual risk after links 1 and 2

Detection is **pull-based**. The banner only appears once someone opens
`/admin`. Nothing pushes a notification, so a delivery failure at 2am is
invisible until the next login. `OPS-3` (uptime monitoring) and `OPS-5`
(alerting) are what make it push-based, and both are still open.

The practical mitigation until then: glance at `/admin/submissions` when you
would otherwise be checking for new enquiries anyway. The banner makes a
problem obvious at that point rather than requiring you to go looking.

---

## 1. Security

| ID | Item | Sev | Effort | Status |
|---|---|---|---|---|
| SEC-1 | Session-based admin auth (replaces Basic Auth) | S1 | L | Open |
| SEC-2 | Verify `ADMIN_PASS` is not still the test value | S1 | S | **Done** |
| SEC-3 | No Content-Security-Policy | S2 | M | **Done** |
| SEC-4 | Public forms have no bot/spam defence | S2 | M | **Done** |
| SEC-5 | No `Referrer-Policy` / `Permissions-Policy` | S3 | S | **Done** |
| SEC-6 | `X-XSS-Protection: 1; mode=block` is deprecated | S3 | S | **Done** |
| SEC-7 | No automated dependency vulnerability scan | S3 | S | Partial |
| SEC-8 | No fail2ban / nginx-level abuse control | S3 | M | Config written |
| SEC-9 | Echo v4.12.0 carries an unpatchable `golang-jwt/jwt` | S3 | M | **Done** |

**SEC-1 — Session auth.** *Open.* `/admin` is still `middleware.BasicAuth`,
which has **no logout** — the browser caches credentials until it is fully
closed, not just the tab.

Decisions locked: bcrypt hash in `.env` as `ADMIN_PASS_HASH`, `admin_sessions`
table storing a *hash* of the token, **24h fixed** expiry, per-IP login rate
limit, rotating the hash invalidates all live sessions, `-hashpw` flag on the
binary. `golang.org/x/crypto` is already an indirect dependency, so bcrypt adds
nothing new to `go.mod`.

Deliberately deferred. The brute-force half of this item was delivered far more
cheaply by the per-IP limiter now on the `/admin` group plus the fail2ban jail,
which leaves **logout** as the only real remaining gap. Whether that matters is
a question about how the panel is actually used — a shared or family computer
makes it urgent, a single personal laptop does not.

**SEC-2 — Credential check.** *Done.* Replaced with a 32-character mixed
alphanumeric password. The `testpass123` default never reached production.

**SEC-3 — CSP.** *Done — enforcing.* `main.go` sets a policy with no
`'unsafe-inline'` for scripts.

The backlog originally called this non-trivial and expected a per-request nonce
threaded through the renderer. An audit of the templates showed that was
unnecessary: the site loads **zero external hosts** (htmx is vendored at
`/public/js/htmx.min.js`; no CDN, fonts, or analytics), and the public pages
carry no inline `<script>` and no inline `style` at all. The entire inline
surface was two duplicated htmx-CSRF listeners and one `onchange` attribute,
all in admin templates. Those moved to `public/js/admin.js` — the `onchange`
became a `document`-delegated listener keyed on `data-autosubmit`, which it had
to be regardless, since the status handler swaps in a freshly rendered `<tr>`.

Two deliberate relaxations, both documented at the policy: `img-src data:` for
the select-arrow SVG embedded in `style.css`, and `style-src-attr
'unsafe-inline'` for the `style="background:{{.Hex}}"` colour swatches, whose
values come from owner-editable DB rows. Splitting `style-src-attr` out keeps
`style-src` itself strict, so an injected `<style>` block is still blocked.

`CSP_REPORT_ONLY=true` switches the header to report-only. Use it on the first
deploy after any policy change: a CSP mistake fails silently and completely —
the browser refuses to run the blocked script and nothing is logged
server-side.

**SEC-4 — Form spam.** *Done.* `handlers/spam.go`, wired into both public form
handlers. Hidden honeypot field plus a three-second minimum submit time; a
rejected submission gets the ordinary success page so a bot cannot tell it was
filtered, and a `spam_rejected` line goes to the log.

Two deliberate design choices:

- The timestamp is **unsigned**, so it only catches bots that POST the instant
  the page parses. Signing it would mean introducing and rotating an
  application secret, which is not proportionate here. The honeypot is the
  load-bearing check.
- A missing or unparseable timestamp is **not** treated as a bot signal, and
  there is no upper time bound. Both avoid the same failure: a wrongly rejected
  submission shows the sender a success message and is then lost silently,
  which is exactly the lead-loss failure §0 was written about. Biased hard
  toward letting spam through over dropping a customer.

The honeypot is hidden by off-screen positioning rather than `display: none`
(the first thing a bot checks for), named `contact_ref` rather than
`company`/`website` so browser autofill never touches it, and carries
`tabindex="-1"` plus `aria-hidden` so no real visitor can reach it.

Escalate to Turnstile/hCaptcha only if spam still gets through.

**SEC-5 — Headers.** *Done.* `Referrer-Policy: strict-origin-when-cross-origin`
and a `Permissions-Policy` denying geolocation, camera, microphone, payment,
USB and interest-cohort. Of the two, only `Referrer-Policy` does real work:
without it the full current URL travels as the `Referer` on any outbound click,
which matters from `/admin/inventory/17/edit`, not from the homepage.
`Permissions-Policy` is defence-in-depth behind the CSP — nothing on the site
requests those APIs.

**SEC-6 — X-XSS-Protection.** *Done — set to `0`.* The header enabled a legacy
browser XSS auditor that was itself a source of vulnerabilities. Every browser
that shipped it has removed it, so this changes nothing at runtime; it is set
explicitly rather than omitted because Echo's default is the deprecated value.

**SEC-7 — Dependency scanning.** *Partial.* `govulncheck` is the official Go
vulnerability scanner (`golang.org/x/vuln`). It resolves the module graph
against the Go vulnerability database and then does **reachability analysis** —
it reports a CVE only if the vulnerable function is actually callable from this
code, which is why it produces far fewer false positives than a manifest
scanner. It uploads no source; only module paths and versions leave the
machine, and those go to the public `vuln.go.dev` API.

Run it before each deploy:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

A pass took the count from 4 reachable vulnerabilities to 1 by upgrading
`x/net` (v0.24.0 → v0.57.0) and `x/text` (v0.14.0 → v0.40.0), which also pulled
`x/crypto` and `x/sys` forward. Note that this raised the `go` directive in
`go.mod` from 1.22 to 1.25.0, which silently broke the Docker build against
`golang:1.22-alpine3.20` — the builder image is now `golang:1.26-alpine`. Watch
for that on any future dependency bump; it is invisible locally because the
installed toolchain simply satisfies the higher directive.

`scripts/admin-smoke.sh` is the manual gate in the meantime: 43 curl-level
checks covering routing, auth, CSRF (both token sources), security headers,
body limits, rate limiting, and the spam defences. Deterministic output, so
`diff baseline.txt after.txt` is the whole review. Still open as an *automated*
gate — see `TEST-3` for CI.

**SEC-8 — fail2ban.** *Config written, not yet installed.* `deploy/fail2ban/`
holds a jail file and an `/admin` auth filter. Jails: `sshd` (the highest-value
target on the box), `prebuilt-admin`, and `nginx-botsearch`. Reads nginx's
access log rather than the app's, because the app logs to a Docker json-file
path that changes with the container ID and cannot be followed across a
redeploy.

**Set `ignoreip` to your own address before enabling** or a mistyped admin
password can lock you out of both `/admin` and SSH.

**SEC-9 — Echo upgrade.** *Done — v4.12.0 → v4.15.4.* Upstream moved JWT
middleware out of core into a separate `echo-jwt` module: `middleware/jwt.go`
exists in v4.12.0 and imports `github.com/golang-jwt/jwt`; in v4.15.4 the file
is gone. `golang-jwt` is now absent from `go.mod` and `go.sum` entirely, which
is the only way that finding could be cleared — it was never a library this
project chose, and no amount of application-side redesign would have removed it
while `echo/v4/middleware` was still imported for Recover/Secure/BodyLimit/
RateLimiter.

`govulncheck` now reports **0 vulnerabilities affecting this code**, down from 4
at the start of the pass. One module-level advisory remains — `GO-2026-5932`,
the unmaintained `golang.org/x/crypto/openpgp` package — but it is a subpackage
this project never imports, and it has no fixed version, so it is noise rather
than a finding. Adding `bcrypt` for SEC-1 will not change that either way.

Also upgraded in tow: `x/time` v0.5.0 → v0.15.0 (the rate limiter's token
bucket), `gommon`, `go-colorable`, `go-isatty`. The `go` directive did **not**
move, so the `golang:1.26-alpine` builder image is still correct.

**Verification.** `scripts/admin-smoke.sh` was run against v4.12.0 to capture a
baseline, then against v4.15.4. The diff is one line — the version banner.
All 43 checks identical, including the ones written specifically for the seams
that made this upgrade risky: `admin.GET("")` still resolving to `/admin`, the
comma-separated CSRF `TokenLookup` still parsing both the form and header
sources, static assets not being shadowed by a group's auto-registered
`RouteNotFound`, and the nested public-3M/admin-40M body limits.

Still outstanding: the browser pass. curl cannot cover htmx swapping a rendered
row in place, the delegated `data-autosubmit` listener surviving that swap,
photo upload/delete, or CSP violations.

---

## 2. Bugs and correctness risks

| ID | Item | Sev | Effort |
|---|---|---|---|
| ~~BUG-1~~ | ~~Email send errors silently discarded~~ — **done** | — | — |
| ~~BUG-2~~ | ~~Email goroutine killed by shutdown~~ — **done** (WaitGroup) | — | — |
| ~~BUG-3~~ | ~~404/500 return JSON~~ — **done** (branded error page + htmx fragment) | — | — |
| ~~BUG-4~~ | ~~No MIME/charset headers~~ — **done** (UTF-8 + Date) | — | — |
| BUG-5 | No email/phone format validation | S3 | S |
| BUG-6 | Partial photo-save failure is invisible (B3) | S3 | M |
| BUG-7 | No SQLite `busy_timeout` | S3 | S | **Done** |
| BUG-8 | Interest form accepts enquiries on sold items | S3 | S | **Done** |
| BUG-9 | Item deletion is irreversible, including photos | S3 | M |

**BUG-1 through BUG-4 — all closed 2026-07-26.** See §0 for the delivery-state
design. Additionally: the error page is branded and `noindex`, htmx requests get
an HTML fragment instead of JSON, HEAD requests get an empty body, and outgoing
mail now carries `Content-Type: text/plain; charset=UTF-8` and a `Date` header.

A fourth bug surfaced while testing the above and is also fixed: `errorHTML`
returned a fragment with no `id="form-response"`, but the public forms swap
`outerHTML` on that target — so a single validation error destroyed the swap
target and the form silently stopped responding to every later submission.

**BUG-5.** *Verified.* Fields are checked for non-empty and length only.
`email` is never format-checked, so a typo'd address is stored and silently
undeliverable. Use `net/mail.ParseAddress`. Keep phone lenient — real people
format numbers inconsistently.

**BUG-7.** *Verified: no pragma set.* `SetMaxOpenConns(1)` serialises the app's
own access, but an external `sqlite3` session (backups, colour-table edits) can
still hold a lock and cause an immediate `SQLITE_BUSY`. Set
`busy_timeout=5000`. Consider WAL at the same time, with the caveat that WAL
adds `-wal`/`-shm` files the backup process must account for.

**BUG-8.** *Verified.* `InstockInterest` looks the item up but never checks
`Status`. A customer can enquire about a shed already marked sold. Return a
clear "already sold" message instead.

**BUG-9.** *Verified.* `AdminDeleteItem` cascades DB rows and `os.RemoveAll`s the
photo directory. `hx-confirm` exists, but one mis-click permanently destroys
photos that may not exist anywhere else. Prefer a `deleted_at` soft delete;
purge on a schedule.

---

**BUG-7 — `busy_timeout`.** *Done.* `PRAGMA busy_timeout = 5000` in
`database.Init`. Without it a write that finds the database locked failed
*instantly* with SQLITE_BUSY rather than waiting. `SetMaxOpenConns(1)`
serialises this process's own access, so the contention that mattered came from
outside it: `scripts/maintenance.sh` runs `.backup` against the live database
nightly, and `sqlite3 prebuilt.db` is the documented way to edit the colour
tables. Either could hold a lock long enough that a contact form submitted at
that moment would have errored and told a real customer to call instead. Five
seconds is far longer than any of those legitimately take, so this converts a
rare hard failure into an unnoticed pause. Verified against the live
connection, not just the source.

**BUG-8 — Sold items.** *Done.* `InstockInterest` re-checks status after
loading the item. Sold sheds deliberately stay on the page with a SOLD ribbon
as proof inventory moves, so the form is still rendered against them — which
makes status precisely the field that can change between render and submit, and
therefore one that cannot be trusted from the client. On-hold is still allowed:
holds fall through regularly, and a second interested buyer is information the
business wants. Mutation-tested.

---

## 3. Operations and reliability

| ID | Item | Sev | Effort |
|---|---|---|---|
| OPS-1 | No automated backups | S1 | M |
| OPS-2 | `scripts/backup.sh` is stale and non-functional | S1 | S |
| OPS-3 | No uptime monitoring | S1 | S |
| OPS-4 | Backup has never been restore-tested | S1 | S |
| OPS-5 | No error alerting | S2 | M |
| OPS-6 | Certbot renewal unverified | S2 | S |
| OPS-7 | No staging environment | S3 | M |
| OPS-8 | Rollback requires a rebuild | S3 | M |
| OPS-9 | No disk-space monitoring | S3 | S |

**OPS-1 / OPS-2.** *Addressed 2026-07-26 — `scripts/maintenance.sh`.* The old
`backup.sh` targeted the pre-Docker layout, so on the deployed server it aborts
with "prebuilt.db not found" and archives nothing. (It fails loudly, not
silently — but from cron that error goes to a log nobody reads, so the result
is the same.) It is now a failing stub pointing at the replacement.

The new script does a consistent `sqlite3 .backup` dump, mirrors photos
incrementally, prunes snapshots, and writes an error digest. **Still
outstanding:** install the crontab entry, and enable off-box sync — `rclone
config` needs an interactive browser login only the owner can complete.
Until that second step is done there is still only one copy, on the same disk
as the original.

**OPS-3.** *Verified: nothing configured.* The site can be down for a day
before anyone notices. UptimeRobot's free tier is ~5 minutes of setup; `/`
already answers `HEAD`, which is what most monitors send.

**OPS-4.** Untested backups are not backups. Restore into a scratch directory
and open it with `sqlite3` before trusting it.

**OPS-5.** `c.Logger().Error(...)` writes to container logs nobody reads.
Anything from a lightweight Sentry SDK to a nightly `grep` of `docker logs`
mailed to the owner beats the current zero.

**OPS-6.** Confirm `systemctl list-timers | grep certbot` and run
`certbot renew --dry-run`. Certificates silently expiring 90 days after launch
is a classic first-quarter outage. **Note the ACME challenge fix** already in
`deploy/nginx.conf.example` is what makes renewal work at all.

**OPS-8.** Deploys rebuild from source on the VPS, so rollback means
`git checkout <sha>` and a full rebuild — minutes of downtime under pressure.
Tagged images in a registry would make rollback a pull.

**OPS-9.** Uploads and logs share the VPS disk. A full disk stops SQLite writes
— i.e. silently stops capturing leads. Alert at 80%.

---

## 4. Performance

| ID | Item | Sev | Effort |
|---|---|---|---|
| PERF-1 | Photos served at full size; no thumbnails | S2 | L |
| PERF-2 | No `loading="lazy"` on images | S2 | S | **Done** |
| PERF-3 | Source images ~0.7–0.9 MB each, unoptimised | S2 | M | Blocked on tooling |
| PERF-4 | Static assets are `no-cache` | S3 | M |
| PERF-5 | No width/height on some images (layout shift) | S3 | S | **Done** |

**PERF-1.** *Verified.* Uploads are capped at 8 MB each and stored as-is; the
public page renders the originals. Twelve photos on one shed is a ~100 MB page
on a phone. Generate thumbnails on upload (`golang.org/x/image` or
`disintegration/imaging`) and serve full size only in the lightbox. **This is
the single largest perceived-quality issue once `/instock` goes live.**

**PERF-2.** *Verified: zero occurrences.* One attribute per `<img>` below the
fold; immediate mobile win.

**PERF-3.** *Verified.* The four largest shipped images are 748–932 KB. Convert
to WebP with JPEG fallback and add `srcset`. Marketing sites live and die on
mobile load time, and this is the homepage's dominant cost.

**PERF-4.** `Cache-Control: no-cache` on `/public/` was the correct fix for
stale-CSS-after-deploy, but it forces a revalidation round-trip per asset per
visit. The proper answer is fingerprinted filenames plus `max-age=31536000,
immutable`. Only worth it once the design settles.

---

**PERF-2 — Lazy loading.** *Done.* 35 below-the-fold images now carry
`loading="lazy" decoding="async"`. This is the large win on this page: the
homepage renders ~35 photos averaging several hundred KB, and every one of them
was previously fetched before the page settled — several megabytes to display a
hero the visitor sees immediately. The nav logo stays eager (above the fold),
and the lightbox image is excluded since it has no `src` until JS sets one.

**PERF-5 — Dimensions.** *Done, with a caveat worth recording.* 36 of 39 `<img>`
tags now carry `width`/`height` read off the actual files. The honest
assessment: this buys **almost no CLS improvement here**, because
`.gallery-img`, `.style-img` and `.instock-img` already pin *both* dimensions in
CSS with `object-fit: cover`, so the layout was never shifting. The attributes
are worth keeping as defence if a fixed height is ever removed, and Lighthouse
flags their absence regardless — but the layout-shift framing in the original
item did not apply to this codebase.

**PERF-3 — Image weight.** *Blocked on tooling, and smaller than assumed.*
Measured rather than estimated:

| Variant | Size |
|---|---|
| Original, progressive JPEG @2048px | 932 KB |
| `sips` re-encode @1600px q80 | **952 KB** (larger) |
| `cwebp` q82 @2048px | **1032 KB** (larger) |
| `cwebp` q82 @1600px | 584 KB |

The sources are already progressive JPEGs and reasonably encoded. `sips`
outputs baseline JPEG and inflates them; WebP at equal dimensions is worse for
these particular photos. The only genuine remaining win is **resizing** — they
are 2048px wide and rendered at a few hundred CSS pixels — which needs a proper
encoder (`mozjpeg` or `jpegoptim`, neither installed). Deferred rather than
done badly. Combined with PERF-2 already deferring the download entirely, the
urgency dropped considerably.

---

## 5. SEO and discoverability

| ID | Item | Sev | Effort |
|---|---|---|---|
| SEO-1 | No Open Graph / Twitter Card tags | S2 | S | **Done** |
| SEO-2 | No `LocalBusiness` structured data | S2 | M | **Done** |
| SEO-3 | No canonical link tags | S3 | S | **Done** |
| SEO-4 | No Google Business Profile linkage | S2 | S | Needs your data |
| SEO-5 | Sitemap is static and hand-maintained | S3 | S | **Done** |
| SEO-6 | No analytics | S3 | S | Needs a decision |

**SEO-1.** *Verified: none present.* Every share of this link on Facebook —
where a shed builder's customers actually are — renders as a bare grey box. One
`<meta>` block in `layout.html` plus a share image fixes it.

**SEO-2.** For a business with physical lots, `LocalBusiness` JSON-LD (address,
phone, hours, service area) is the highest-leverage SEO available. Directly
feeds map/local results.

**SEO-4.** Local search for this business type is dominated by Google Business
Profile, not the website. Claim it, link both directions, keep NAP (name,
address, phone) byte-identical with the site.

**SEO-5.** `public/sitemap.xml` is static with the `/instock` entry commented
out awaiting `FEATURE_INSTOCK`. Easy to forget. Generating it from the router
and flag state removes the coupling.

**SEO-6.** No analytics at all — there is currently no way to know whether the
site produces business. Plausible/Fathom avoid a cookie banner (see `LEGAL-1`);
GA4 does not.

---

**SEO-1 / SEO-3 — Open Graph, Twitter Cards, canonical.** *Done.* Both need
absolute URLs, so `SITE_URL` was added to `Config` and exposed to templates as
`siteURL`. The whole block is gated on it being set: with `SITE_URL` unset the
tags are omitted entirely rather than emitted with an empty origin, because a
canonical pointing nowhere actively tells search engines the real page lives
somewhere else — strictly worse than having none. `twitter:card` is
`summary_large_image`, which is what turns a shared link into a full-width photo
rather than a thumbnail; for a business that sells on how its work looks, that
is the entire point.

Outstanding: `og:image` currently reuses `hero-image.jpg`. A purpose-made
1200×630 crops far better — Facebook and LinkedIn letterbox anything far from
that ratio. Marked with a `REPLACE` comment in `layout.html`.

---

**SEO-2 / SEO-4 — Structured data and Business Profile.** *Partially done —
needs data only you have.* `templates/partials/structured_data.html` emits
`GeneralContractor` JSON-LD on both public pages, gated on `SITE_URL` because
`@id` and `url` must be absolute. Validated in a test: malformed JSON-LD is
silently ignored by search engines, with no error anywhere — the rich result
just never appears.

Populated from verifiable facts only: name, url, logo, image, description,
telephone, and Mon–Sat 08:00–18:00 hours taken from `contact.html`.

**`address`, `areaServed` and `sameAs` are deliberately omitted, not
forgotten.** Google cross-checks name/address/phone against the Business
Profile, and a mismatch *suppresses* local ranking rather than merely failing to
help — a guessed address is an active penalty. The file carries instructions;
`sameAs` holding the Business Profile URL is the actual site-to-profile link
that SEO-4 asks for. A test asserts these stay absent, and names itself as the
thing to remove once the real values are in.

Note: `application/ld+json` is a data block, not executable script, so
`script-src 'self'` does not block it — no nonce or policy change needed.

---

**SEO-5 — Sitemap generated, not stored.** *Done.* `public/sitemap.xml` and
`public/robots.txt` are deleted; both are now built per-request in
`handlers/seo.go` and wired in `server.go`.

The `/instock` entry is emitted from the same `featureInstock` value that
decides whether the route is registered at all, passed into the handler rather
than re-read, so the two cannot disagree. Previously the entry sat commented out
in the XML with a note to uncomment it when the flag went on — a manual step
whose only failure mode was nobody remembering. Both directions are wrong and
both were reachable: a sitemap advertising a URL that 404s is a Search Console
error, and one omitting a live page is a page Google may never crawl.

Two things fixed in passing. Both files hardcoded
`https://prebuiltshedsllc.com`, so any non-production deploy would have handed
Google the production domain; they now use `SITE_URL`, falling back to the
request's host so dev and CI need no configuration. And serving them out of
`public/` left them reachable twice — `/sitemap.xml` and `/public/sitemap.xml`
— which is a free duplicate-content signal. `/public/sitemap.xml` now 404s.

The host fallback is safe here in a way it would *not* be for the canonical and
`og:url` tags, which stay gated on `SITE_URL`. The difference is who reads the
output. A forged `Host` on a canonical tag misdirects other people's traffic,
because the tag is embedded in a page real visitors fetch. A sitemap is fetched
directly by the crawler, which sends the true `Host`, so forging it only returns
a bogus document to whoever forged it.

`Disallow` also changed from `/admin/` to `/admin`. robots.txt matching is a
plain prefix match, so the trailing slash left `/admin` itself — the auth prompt
— crawlable.

No `<lastmod>`: the honest value would need content change times this server
does not track, and a `lastmod` that is always "now" teaches crawlers to ignore
the field entirely.

---

## 6. Accessibility

| ID | Item | Sev | Effort |
|---|---|---|---|
| A11Y-1 | No skip-to-content link | S3 | S |
| A11Y-2 | Colour contrast unaudited | S3 | M |
| A11Y-3 | Menu/modal focus not trapped | S3 | M |
| A11Y-4 | No reduced-motion handling | S3 | S |
| A11Y-5 | Alt text unaudited on real photos | S3 | S |

Baseline is better than typical: all six contact inputs have real `<label>`s,
the nav exposes `aria-expanded`/`aria-controls`, and the recent
`visibility: hidden` fix removed the invisible-but-focusable overlay bug in the
nav, lightbox, and modal.

**A11Y-2.** `--text-muted: #9A9A9A` and `--chrome-dark: #8A8A8A` on dark
backgrounds are likely below the 4.5:1 WCAG AA threshold. Worth measuring.

**A11Y-3.** Overlays close on Escape and outside-click but do not *trap* focus,
so Tab can walk behind an open modal.

**A11Y-4.** Carousels auto-rotate every 4.5s with no
`prefers-reduced-motion` guard — a genuine vestibular-trigger issue.

---

## 7. Product and feature gaps

| ID | Item | Sev | Effort |
|---|---|---|---|
| ~~FEAT-1~~ | ~~No admin view of contact submissions~~ — **done** | — | — |
| FEAT-2 | Launch `/instock` (flag currently off) | S2 | S |
| FEAT-3 | Real content: photos, reviews, pricing, colours | S2 | L |
| FEAT-4 | No colour reference-table admin UI | S3 | M |
| FEAT-5 | Testimonials hardcoded | S3 | M |
| FEAT-6 | No admin audit log | S3 | M |
| FEAT-7 | No inventory search/filter/pagination | S4 | M |
| FEAT-8 | No customer-facing confirmation email | S3 | S |
| FEAT-9 | No privacy policy / terms pages | S3 | S |
| ~~FEAT-10~~ | ~~No 404 page content~~ — **done** | — | — |

**FEAT-1.** *Verified: no handler or template reads `contact_submissions`.* The
site's single most important business record is invisible to its owner. A
read-only list with date, name, contact details, message, and CSV export is
half a day and closes the `§0` chain.

**FEAT-3.** Placeholders remain across testimonials, several images, pricing,
and the seeded colour codes. No code is blocked — this is content acquisition,
expected to land gradually.

**FEAT-4.** *Verified: no UI.* Colours are edited via `sqlite3` inside the
container, which is not a workflow a non-technical owner can run. Blocks
handing over inventory management.

**FEAT-8.** An auto-reply materially improves perceived professionalism and
gives the customer a record. Note it changes the email risk profile — mail then
goes to *customers*, not just the owner's own inbox, which is when SPF/DKIM on
the real domain starts to matter (currently Gmail-to-self, where it does not).

---

## 8. Technical debt

| ID | Item | Sev | Effort | Status |
|---|---|---|---|---|
| DEBT-1 | Error fragments bypass `html/template` (Q2) | S3 | M | Open |
| DEBT-2 | `main.go` is doing too much | S3 | M | **Done** |
| DEBT-3 | Config read via scattered `os.Getenv` | S3 | S | Partial |
| DEBT-4 | No DB layer interface; handlers call package globals | S3 | L | Open |
| DEBT-5 | Inline styles/scripts in admin templates | S3 | S | Partial |

**DEBT-1.** Full analysis in `AUDIT_CHECKLIST.md`. Security motivation is
already satisfied by centralised escaping; what remains is consistency. The real
risk is the htmx swap contract (`hx-swap="outerHTML"` against
`#form-response`) — the fragment must keep an identical outer element.

**DEBT-2.** *Done.* Split into `config.go` (settings), `server.go`
(`newServer`), and a 56-line `main()` holding only process lifecycle. See
`TEST-6` for what that unlocked.

**DEBT-3.** *Partial.* The main package's nine scattered `os.Getenv` calls are
now a single `Config` resolved by `loadConfig()`. The SMTP settings in
`handlers/contact.go` are still read inline; moving those means threading
config into package-level handler functions, which is `DEBT-4`.

**DEBT-5.** *Partial.* Inline `<script>` blocks are gone — moved to
`public/js/admin.js` so the CSP needs no `'unsafe-inline'` for scripts. Inline
`style="background:{{.Hex}}"` colour swatches remain, covered deliberately by
`style-src-attr`; their values come from owner-editable DB rows, so a CSS class
per colour is not available.

**DEBT-3.** Env vars are read at point of use across several files. A single
`config` struct parsed once at startup makes misconfiguration a boot-time error
rather than a runtime surprise.

**DEBT-4.** `database.DB` is a package global, so handlers cannot be tested
against a fake. This is the main reason test coverage stops at pure functions.
Blocks `TEST-1`.

---

## 9. Testing and CI

| ID | Item | Sev | Effort | Status |
|---|---|---|---|---|
| TEST-1 | No handler or DB integration tests | S2 | L | Partial |
| TEST-2 | No CI pipeline | S2 | S | **Done** |
| TEST-3 | No `govulncheck` | S3 | S | **Done** |
| TEST-4 | Docker image build never verified | S2 | S | **Done** |
| TEST-5 | No smoke test after deploy | S3 | M | Open |
| TEST-6 | `main()` is not testable | S3 | M | **Done** |

**TEST-1 — Coverage.** *Partial.* Six Go tests over pure functions
(`GenerateCode`, `Describe`, `checkPhotoCap`, and three over
`parseInventoryForm`), plus 43 end-to-end checks in `scripts/admin-smoke.sh`
covering routing, auth, CSRF via both token sources, security headers, body
limits, rate limiting, and the spam defences.

`TEST-6` unblocked the Go side: `server_test.go` adds 15 tests against the real
wiring, three of them mutation-tested against regressions this project actually
shipped. What stays in bash is what needs a real process and filesystem —
rate-limiter burst behaviour over wall-clock time, the CSRF round trip through
a cookie jar, and persistence assertions via `sqlite3`.

**TEST-2 — CI.** *Done.* `.github/workflows/ci.yml`, three jobs:

| Job | Runs |
|---|---|
| `check` | `fmt-check`, `vet`, `test`, `check-docker-go`, `smoke` |
| `vuln` | `govulncheck` |
| `docker` | build the image, run it, assert it serves |

`vuln` is deliberately its own job: a newly published upstream CVE turns it red
with no change to this repository, and that must be distinguishable at a glance
from a genuine regression.

Every step is a `make` target and the workflow contains no commands of its own.
When a pipeline and a developer run different commands they drift, and "passes
locally, red in CI" becomes routine — `make ci` reproduces the whole pipeline
in about 13 seconds locally.

`setup-go` reads `go-version-file: go.mod`, so a dependency upgrade that raises
the directive moves CI with it automatically.

**`make check-docker-go`** guards a bug this project actually shipped: `go get`
raised the go directive from 1.22 to 1.25 while the Dockerfile still built on
`golang:1.22-alpine`. Nothing failed locally, because the installed toolchain
simply satisfied the higher directive; it would have surfaced as a broken
deploy. The guard was negative-tested by temporarily reverting the Dockerfile
and confirming it fails.

**TEST-4 — Image verified.** *Done.* Built and run end to end for the first
time. 71.2MB; runs as uid 10001; templates and static assets resolve from the
image's WORKDIR; the database is created in the named volume with correct
ownership and no chown step; HEALTHCHECK reports `healthy`; SIGTERM shuts down
in 0.23s rather than hitting the 10s grace timeout, so graceful shutdown works.
`make docker-smoke` now asserts all of this on every CI run.

**TEST-5 — Post-deploy smoke.** *Open.* Distinct from CI, which tests the
*artifact*. This tests the *deployment*: HSTS actually arriving through nginx,
`TRUST_PROXY` yielding real client IPs (if it regresses, every visitor shares
one rate-limit bucket and real customers collectively 429), volumes mounted,
`/data` writable, TLS valid. None of it is visible to CI.

**TEST-6 — `main()` is untestable.** *Done.* `main()` went from 345 lines to
56: configuration in `config.go`, all wiring in `newServer(cfg) (*echo.Echo,
error)` in `server.go`, and `main()` reduced to process lifecycle — config,
storage, signals, shutdown. `database.Init` now takes the path as a parameter
instead of reading `DB_PATH`, so a caller can point at a temporary file without
mutating the environment. This also closes `DEBT-2` and the main-package half
of `DEBT-3`.

`server_test.go` adds 15 tests against the instance `newServer` actually
builds. That distinction is the entire point: middleware ordering, the
catch-all `RouteNotFound` a middleware-bearing `Group` auto-registers,
`admin.GET("")` path joining, and the nested public-3M/admin-40M body limits
are properties of `newServer` and nothing else. A test that constructed its own
`echo.New()` and re-declared the middleware would assert against a copy and
stay green through exactly the regressions worth catching.

Three were mutation-tested rather than assumed:

| Mutation | Caught by |
|---|---|
| `e.Group("/public", mw).Static(...)` — the change that 404'd every asset | `TestStaticAssetsAreNotShadowed` |
| Basic Auth returning `true` on blank credentials | `TestAdminFailsClosedWhenUnconfigured` |
| `BodyLimit("3M")` applied globally | `TestBodyLimitsAreScopedNotGlobal` |

`template.Must` was also replaced with returned errors, so a glob matching
nothing — what a bad Dockerfile `COPY` produces — reports which pattern failed
instead of panicking with a stack trace.

Remaining in bash (`scripts/admin-smoke.sh`): the checks needing a real process
and filesystem — rate-limiter burst behaviour over wall-clock time, the CSRF
round trip through a cookie jar, and end-to-end persistence assertions via
`sqlite3`.

### Deployment pipeline — decided, not yet built

CD is deliberately **not** automated yet, and the blocker is `OPS-1`: there are
no backups. Automating deploys against a SQLite database with no recovery path
is the wrong order of operations. CI is safe to run today; CD follows backups.

Decisions locked for when it is built:

- **CI builds the image and pushes to GHCR** (`ghcr.io/huntermotko/prebuilt`);
  the VPS pulls. The VPS is small, and compiling Go on it is slow and can OOM.
  This also makes the artifact CI tested byte-identical to the one that runs.
  Requires changing `docker-compose.yml` from `build:` to `image:`, a
  `packages: write` permission, and an SSH key in repository secrets.
- **Triggered by `workflow_dispatch`, not merge to `main`.** Single VPS, no
  staging environment: auto-deploy on merge means a bad push takes down the
  site that generates the business's leads, with nobody watching.
- Followed by the `TEST-5` post-deploy smoke against the live URL.

---

## 10. Legal and compliance

| ID | Item | Sev | Effort |
|---|---|---|---|
| LEGAL-1 | No privacy policy | S2 | S |
| LEGAL-2 | No stated data-retention policy | S3 | S |
| LEGAL-3 | No cookie notice | S3 | S |

The site collects name, phone, and email and stores them indefinitely. For a
US small business this is low-risk, but a short privacy page is cheap, expected
by customers, and required by Google Ads if that is ever used. Currently the
only cookie is the admin CSRF token — strictly necessary, so no banner is
needed *provided* analytics stays cookieless (`SEO-6`).

---

## 11. Suggested sequence

**Week 1 — stop losing money.** `SEC-2` (verify the password, today) ·
`BUG-1` · `OPS-3` · `OPS-2`/`OPS-1` · `FEAT-1` · `BUG-3`

Together these close the `§0` lead-loss chain, make the site's failure modes
visible, and remove the JSON 404 a customer can hit right now.

**Week 2 — harden.** `SEC-1` session auth (retires `SEC-2` permanently) ·
`OPS-4` restore test · `OPS-6` certbot verification · `TEST-2` CI · `BUG-4`,
`BUG-5`, `BUG-7`

**Week 3 — grow.** `SEO-1`, `SEO-2`, `SEO-4` · `PERF-2`, `PERF-3` ·
`FEAT-9`/`LEGAL-1` · `SEO-6` analytics

**Before `/instock` launches.** `PERF-1` thumbnails (mandatory — the page is
unusable on mobile without it) · `FEAT-4` colour admin · `BUG-8` sold-item
guard · `SEO-5` sitemap · then flip `FEATURE_INSTOCK`.

**Ongoing.** `SEC-3` CSP · `DEBT-*` · `A11Y-*` · `TEST-1`

---

## 12. Verified healthy

Not everything needs work. Confirmed sound in this pass, worth not regressing:

- CSRF on every admin mutation, covering both form posts and htmx, with the
  cookie explicitly path-scoped to `/admin`
- Upload validation completes before any commit — no orphaned rows or
  half-applied edits; content type sniffed from bytes; filenames
  server-generated
- Currency stored as `int64` cents with `math.Round`, regression-tested
- DB `CHECK` constraints with idempotent, guarded migrations following SQLite's
  table-rebuild procedure
- `SetMaxOpenConns(1)` so `PRAGMA foreign_keys` and the photo cascade actually
  hold
- HSTS explicitly configured (Echo's default `HSTSMaxAge` of `0` silently
  disables it)
- Per-IP rate limiting with correct client-IP recovery behind nginx
- Graceful shutdown, server timeouts, non-root container, loopback-only bind
- Feature flags gate *routes*, not just links
- Destructive admin actions confirm via `hx-confirm`
- All contact inputs have real `<label>` elements
