package main

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

// These tests exercise the instance newServer actually builds, not a
// reconstruction of it. That is the whole point of the split: middleware
// ordering, the catch-all RouteNotFound that a middleware-bearing Group
// auto-registers, admin.GET("") path joining, and the nested body limits are
// properties of newServer alone. A test that wired its own echo.New() would
// assert against a copy and stay green through exactly the regressions worth
// catching.
//
// Templates and static assets resolve by relative path, and `go test` runs with
// the package directory as the working directory, so these find them the same
// way the real binary does.

const (
	testAdminUser = "testadmin"
	testAdminPass = "testpass"
)

// TestMain points the package-level database at a temp file so tests never
// touch a real prebuilt.db. Init is called once because the handlers reach the
// database through a package global.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "prebuilt-test")
	if err != nil {
		panic(err)
	}
	database.Init(filepath.Join(dir, "test.db"))

	code := m.Run()

	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func testConfig() Config {
	return Config{
		Port:      "0",
		AdminUser: testAdminUser,
		AdminPass: testAdminPass,
	}
}

func newTestServer(t *testing.T, cfg Config) *echo.Echo {
	t.Helper()
	e, err := newServer(cfg)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return e
}

// do runs a request through the full middleware chain.
func do(t *testing.T, e *echo.Echo, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, e *echo.Echo, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, e, httptest.NewRequest(http.MethodGet, path, nil))
}

func TestPublicRoutes(t *testing.T) {
	e := newTestServer(t, testConfig())

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/", http.StatusOK},
		// Registered for HEAD as well as GET because uptime monitors commonly
		// probe with HEAD, and a GET-only route answers with a 405 that reads
		// as an outage.
		{http.MethodHead, "/", http.StatusOK},
		{http.MethodGet, "/robots.txt", http.StatusOK},
		{http.MethodGet, "/sitemap.xml", http.StatusOK},
		{http.MethodGet, "/nonexistent-page", http.StatusNotFound},
	} {
		rec := do(t, e, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

// Regression test for a real outage: attaching middleware to a group makes Echo
// auto-register a catch-all RouteNotFound for that prefix. When the
// Cache-Control middleware was briefly mounted via e.Group("/public", ...) that
// catch-all shadowed the static route and 404'd every asset on the site.
func TestStaticAssetsAreNotShadowed(t *testing.T) {
	e := newTestServer(t, testConfig())

	for _, path := range []string{
		"/public/css/style.css",
		"/public/js/main.js",
		"/public/js/admin.js",
		"/public/js/htmx.min.js",
	} {
		rec := get(t, e, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (static route shadowed?)", path, rec.Code)
		}
		// Static URLs are never fingerprinted, so without this a browser can
		// keep serving a stale style.css across a deploy.
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want %q", path, got, "no-cache")
		}
	}
}

// Echo's default error handler emits JSON, which rendered {"message":"Not
// Found"} on a marketing site and got swapped into the page by htmx on a 429.
func TestErrorsRenderHTMLNotJSON(t *testing.T) {
	e := newTestServer(t, testConfig())

	rec := get(t, e, "/definitely-not-a-page")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("404 Content-Type = %q, want text/html", ct)
	}
	if strings.Contains(rec.Body.String(), `"message"`) {
		t.Error("404 body looks like Echo's JSON default")
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newTestServer(t, testConfig())
	rec := get(t, e, "/")

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "SAMEORIGIN",
		// Not "1; mode=block": that enabled a legacy auditor which was itself a
		// source of vulnerabilities.
		"X-XSS-Protection": "0",
		"Referrer-Policy":  "strict-origin-when-cross-origin",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	if rec.Header().Get("Permissions-Policy") == "" {
		t.Error("Permissions-Policy is missing")
	}

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy is missing")
	}
	// The value of this CSP is that scripts cannot be inlined. If
	// 'unsafe-inline' ever lands in script-src the policy stops being a
	// meaningful XSS defence while still looking present.
	if strings.Contains(csp, "script-src") && strings.Contains(csp, "'unsafe-inline'") {
		scriptSrc := csp[strings.Index(csp, "script-src"):]
		if end := strings.Index(scriptSrc, ";"); end > 0 {
			scriptSrc = scriptSrc[:end]
		}
		if strings.Contains(scriptSrc, "'unsafe-inline'") {
			t.Errorf("script-src permits 'unsafe-inline': %q", scriptSrc)
		}
	}
}

// HSTS must stay dormant on plain HTTP — a Strict-Transport-Security header
// delivered over http:// is ignored, and asserting it here would hide the fact
// that Echo only emits it when it believes the connection is secure.
func TestHSTSOnlyOverTLS(t *testing.T) {
	e := newTestServer(t, testConfig())

	if got := get(t, e, "/").Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS sent over plain HTTP: %q", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := do(t, e, req).Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("HSTS missing behind X-Forwarded-Proto: https")
	}
}

func TestCSPReportOnlyFlag(t *testing.T) {
	cfg := testConfig()
	cfg.CSPReportOnly = true
	e := newTestServer(t, cfg)

	rec := get(t, e, "/")
	if rec.Header().Get("Content-Security-Policy-Report-Only") == "" {
		t.Error("CSP_REPORT_ONLY did not switch the header to report-only")
	}
	if rec.Header().Get("Content-Security-Policy") != "" {
		t.Error("enforcing CSP still sent while in report-only mode")
	}
}

// The flag must remove the routes, not merely hide the nav link: an unlinked
// but live /instock is still reachable by guessing the URL, and it carries a
// working interest form.
func TestInstockFeatureFlag(t *testing.T) {
	off := newTestServer(t, testConfig())
	if rec := get(t, off, "/instock"); rec.Code != http.StatusNotFound {
		t.Errorf("flag off: GET /instock = %d, want 404", rec.Code)
	}
	rec := do(t, off, httptest.NewRequest(http.MethodPost, "/instock/interest", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("flag off: POST /instock/interest = %d, want 404", rec.Code)
	}

	cfg := testConfig()
	cfg.FeatureInstock = true
	if rec := get(t, newTestServer(t, cfg), "/instock"); rec.Code != http.StatusOK {
		t.Errorf("flag on: GET /instock = %d, want 200", rec.Code)
	}
}

// The sitemap and the router must agree about /instock. Before this was
// generated, public/sitemap.xml carried the /instock entry commented out with a
// note to uncomment it when the flag went on — a manual step with no failure
// mode other than someone noticing. Flipping the flag now has to move both.
func TestSitemapTracksFeatureFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
	}{{"flag off", false}, {"flag on", true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.FeatureInstock = tc.enabled
			e := newTestServer(t, cfg)

			body := get(t, e, "/sitemap.xml").Body.String()

			// Parsed, not substring-matched: a sitemap search engines reject is
			// indistinguishable from one they never fetched, so "contains the
			// right text" is not the property worth asserting.
			var set struct {
				URLs []struct {
					Loc string `xml:"loc"`
				} `xml:"url"`
			}
			if err := xml.Unmarshal([]byte(body), &set); err != nil {
				t.Fatalf("sitemap is not valid XML: %v\n%s", err, body)
			}

			var locs []string
			for _, u := range set.URLs {
				locs = append(locs, u.Loc)
			}

			listed := slices.ContainsFunc(locs, func(l string) bool {
				return strings.HasSuffix(l, "/instock")
			})
			if listed != tc.enabled {
				t.Errorf("/instock listed = %v, want %v (locs: %v)", listed, tc.enabled, locs)
			}

			// The homepage is listed either way, and every <loc> must be
			// absolute — the sitemap format has no relative form.
			if len(locs) == 0 {
				t.Fatal("sitemap has no <url> entries")
			}
			for _, l := range locs {
				if !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
					t.Errorf("<loc> %q is not absolute", l)
				}
			}
		})
	}
}

// SITE_URL is unset in dev and CI, so both documents fall back to the request's
// host. Production sets it, and it must win — otherwise a request arriving with
// any other Host would advertise that origin to a crawler.
func TestSEODocsPreferSiteURL(t *testing.T) {
	cfg := testConfig()
	cfg.SiteURL = "https://prebuiltshedsllc.com"
	e := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	req.Host = "attacker.example"
	if body := do(t, e, req).Body.String(); strings.Contains(body, "attacker.example") {
		t.Errorf("sitemap used the Host header over SITE_URL:\n%s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	req.Host = "attacker.example"
	body := do(t, e, req).Body.String()
	if !strings.Contains(body, "Sitemap: https://prebuiltshedsllc.com/sitemap.xml") {
		t.Errorf("robots.txt did not advertise the SITE_URL sitemap:\n%s", body)
	}
	// A trailing slash on Disallow would leave /admin itself crawlable, since
	// robots.txt matching is a plain prefix match.
	if !strings.Contains(body, "Disallow: /admin\n") {
		t.Errorf("robots.txt does not disallow /admin:\n%s", body)
	}
}

// Regression test for a red CI run on main. POST /contact returns as soon as
// the row is saved and then writes the email status from a goroutine; under
// SQLite's default rollback-journal mode that write takes an EXCLUSIVE lock
// that blocks every reader, so anything querying the database in that window
// failed with "database is locked". Under WAL, readers never block on a writer.
//
// Asserted by reading the mode back rather than trusting the statement:
// journal_mode returns the mode actually in force, and SQLite reports success
// while silently keeping the old one if the switch cannot be made.
func TestDatabaseUsesWAL(t *testing.T) {
	var mode string
	if err := database.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("reading journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestAdminRequiresAuth(t *testing.T) {
	e := newTestServer(t, testConfig())

	for _, path := range []string{
		"/admin",
		"/admin/submissions",
		"/admin/inventory/new",
	} {
		if rec := get(t, e, path); rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated GET %s = %d, want 401", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("wrong", "wrong")
	if rec := do(t, e, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad credentials = %d, want 401", rec.Code)
	}

	// admin.GET("") must resolve to exactly "/admin". If Echo's group path
	// joining ever changes this 404s while every sub-route keeps working —
	// a failure that reads like a template bug.
	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth(testAdminUser, testAdminPass)
	if rec := do(t, e, req); rec.Code != http.StatusOK {
		t.Errorf("authenticated GET /admin = %d, want 200", rec.Code)
	}
}

// Blank credentials must reject everything rather than admit everyone. This is
// the difference between a misconfigured deploy being unusable and it being
// wide open.
func TestAdminFailsClosedWhenUnconfigured(t *testing.T) {
	e := newTestServer(t, Config{Port: "0"})

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("", "")
	if rec := do(t, e, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("empty configured credentials = %d, want 401", rec.Code)
	}
}

// Basic Auth credentials auto-attach to same-origin requests regardless of
// which page triggered them, so /admin needs CSRF on top of auth.
func TestAdminRejectsWriteWithoutCSRF(t *testing.T) {
	e := newTestServer(t, testConfig())

	req := httptest.NewRequest(http.MethodPost, "/admin/inventory",
		strings.NewReader("lot=1&style=Gable"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(testAdminUser, testAdminPass)

	if rec := do(t, e, req); rec.Code != http.StatusBadRequest {
		t.Errorf("POST without CSRF token = %d, want 400", rec.Code)
	}
}

// The CSRF cookie must be scoped to all of /admin. Without an explicit path it
// defaults to the directory of whichever admin sub-path first set it, so a
// token picked up on one admin page silently fails on another.
func TestCSRFCookieScope(t *testing.T) {
	e := newTestServer(t, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/admin/inventory/new", nil)
	req.SetBasicAuth(testAdminUser, testAdminPass)
	rec := do(t, e, req)

	for _, c := range rec.Result().Cookies() {
		if !strings.Contains(strings.ToLower(c.Name), "csrf") {
			continue
		}
		if c.Path != "/admin" {
			t.Errorf("CSRF cookie path = %q, want /admin", c.Path)
		}
		if !c.HttpOnly {
			t.Error("CSRF cookie is not HttpOnly")
		}
		return
	}
	t.Error("no CSRF cookie was set on an admin page")
}

func TestCSRFCookieSecureFollowsConfig(t *testing.T) {
	cfg := testConfig()
	cfg.CookieSecure = true
	e := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/admin/inventory/new", nil)
	req.SetBasicAuth(testAdminUser, testAdminPass)

	for _, c := range do(t, e, req).Result().Cookies() {
		if strings.Contains(strings.ToLower(c.Name), "csrf") && !c.Secure {
			t.Error("COOKIE_SECURE=true did not mark the CSRF cookie Secure")
		}
	}
}

// The public 3M limit and the admin 40M limit are nested, and BodyLimit wraps
// the body reader — a stricter outer wrap cannot be loosened by a looser inner
// one. That is why the small limit is scoped to public routes instead of being
// applied globally, and why this asserts both directions.
func TestBodyLimitsAreScopedNotGlobal(t *testing.T) {
	e := newTestServer(t, testConfig())
	oversize := strings.Repeat("x", 4<<20) // 4MB: over the public cap, under admin's

	req := httptest.NewRequest(http.MethodPost, "/contact", strings.NewReader(oversize))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := do(t, e, req); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("4MB to /contact = %d, want 413", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/inventory", strings.NewReader(oversize))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(testAdminUser, testAdminPass)
	// 400 from the CSRF check, not 413: the body was allowed through, which is
	// the property under test.
	if rec := do(t, e, req); rec.Code == http.StatusRequestEntityTooLarge {
		t.Error("4MB to /admin was rejected as too large; the public 3M limit leaked")
	}
}

func TestTrustProxyControlsIPExtraction(t *testing.T) {
	off := newTestServer(t, testConfig())
	if off.IPExtractor != nil {
		t.Error("IP extractor installed with TRUST_PROXY off — client IPs would be spoofable")
	}

	cfg := testConfig()
	cfg.TrustProxy = true
	if newTestServer(t, cfg).IPExtractor == nil {
		t.Error("TRUST_PROXY on did not install the X-Forwarded-For extractor")
	}
}

// A missing template directory previously panicked with a stack trace, which is
// what a bad Dockerfile COPY produces.
func TestNewServerReportsTemplateErrors(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	}()

	if _, err := newServer(testConfig()); err == nil {
		t.Error("newServer succeeded with no templates on disk")
	}
}

// SEO tags must carry absolute URLs, and must be omitted entirely rather than
// emitted with an empty origin. A canonical pointing at nothing actively tells
// search engines the real page lives elsewhere — strictly worse than none.
func TestSEOTagsRequireSiteURL(t *testing.T) {
	withoutURL := get(t, newTestServer(t, testConfig()), "/").Body.String()
	for _, tag := range []string{"canonical", "og:url", "og:image", "twitter:card"} {
		if strings.Contains(withoutURL, tag) {
			t.Errorf("%s emitted with SITE_URL unset", tag)
		}
	}

	cfg := testConfig()
	cfg.SiteURL = "https://example.com"
	body := get(t, newTestServer(t, cfg), "/").Body.String()

	for _, want := range []string{
		`<link rel="canonical" href="https://example.com/">`,
		`property="og:url" content="https://example.com/"`,
		`content="https://example.com/public/images/hero-image.jpg"`,
		`name="twitter:card" content="summary_large_image"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing SEO tag: %s", want)
		}
	}

	// A trailing slash on SITE_URL must not produce "https://example.com//".
	cfg.SiteURL = "https://example.com/"
	cfg2 := loadConfigFrom(cfg.SiteURL)
	if strings.HasSuffix(cfg2, "/") {
		t.Errorf("SiteURL kept its trailing slash: %q", cfg2)
	}
}

// loadConfigFrom exercises the trailing-slash trimming in loadConfig without
// depending on process-wide environment state in a parallel test run.
func loadConfigFrom(raw string) string {
	prev := os.Getenv("SITE_URL")
	defer os.Setenv("SITE_URL", prev)
	os.Setenv("SITE_URL", raw)
	return loadConfig().SiteURL
}

// Sold sheds stay on the page with a SOLD ribbon, as visible proof that
// inventory moves, so the interest form is still rendered against them. Status
// is therefore exactly the field that can change between render and submit, and
// has to be re-checked server-side.
func TestInterestFormRejectsSoldItems(t *testing.T) {
	cfg := testConfig()
	cfg.FeatureInstock = true
	e := newTestServer(t, cfg)

	inStockID, err := database.CreateInventoryItem(database.InventoryItem{
		Lot: 1, Style: database.StyleGable, Width: 12, Length: 24,
		SidingCode: "30", RoofCode: "20", Status: database.StatusInStock,
	})
	if err != nil {
		t.Fatalf("seed in-stock item: %v", err)
	}
	soldID, err := database.CreateInventoryItem(database.InventoryItem{
		Lot: 1, Style: database.StyleGable, Width: 12, Length: 24,
		SidingCode: "30", RoofCode: "20", Status: database.StatusInStock,
	})
	if err != nil {
		t.Fatalf("seed sold item: %v", err)
	}
	if err := database.UpdateInventoryItemStatus(soldID, database.StatusSold); err != nil {
		t.Fatalf("mark sold: %v", err)
	}

	post := func(itemID int64) *httptest.ResponseRecorder {
		form := "item_id=" + strconv.FormatInt(itemID, 10) +
			"&name=Jane&phone=5550000&email=jane@example.com" +
			"&form_ts=" + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
		req := httptest.NewRequest(http.MethodPost, "/instock/interest", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return do(t, e, req)
	}

	if rec := post(inStockID); rec.Code != http.StatusOK {
		t.Errorf("in-stock item = %d, want 200", rec.Code)
	}

	rec := post(soldID)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("sold item = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "has sold") {
		t.Errorf("sold item response did not explain why: %s", rec.Body.String())
	}
}

// Malformed JSON-LD is silently ignored by search engines — there is no error
// anywhere, the rich result simply never appears. That makes it exactly the
// kind of thing worth asserting in a test rather than eyeballing.
func TestStructuredDataIsValidJSON(t *testing.T) {
	if body := get(t, newTestServer(t, testConfig()), "/").Body.String(); strings.Contains(body, "application/ld+json") {
		t.Error("structured data emitted with SITE_URL unset; @id and url would be relative")
	}

	cfg := testConfig()
	cfg.SiteURL = "https://example.com"
	body := get(t, newTestServer(t, cfg), "/").Body.String()

	m := regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no JSON-LD block rendered")
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(m[1]), &data); err != nil {
		t.Fatalf("JSON-LD is not valid JSON: %v", err)
	}

	if data["name"] != "Prebuilt Sheds LLC" {
		t.Errorf("name = %v", data["name"])
	}
	if data["url"] != "https://example.com/" {
		t.Errorf("url = %v", data["url"])
	}

	// Address, areaServed and sameAs must stay absent until they can be copied
	// verbatim from the Google Business Profile. A NAP mismatch actively
	// suppresses local ranking, so a guessed address is worse than none.
	for _, k := range []string{"address", "areaServed", "sameAs"} {
		if _, present := data[k]; present {
			t.Errorf("%q is populated — verify it matches the Business Profile exactly, then remove this assertion", k)
		}
	}
}

// Images below the fold must not block the initial load. The homepage carries
// ~35 photos at several hundred KB each; without lazy loading every one is
// fetched before the page settles.
func TestImagesAreLazyLoaded(t *testing.T) {
	body := get(t, newTestServer(t, testConfig()), "/").Body.String()

	imgs := regexp.MustCompile(`<img\b[^>]*>`).FindAllString(body, -1)
	if len(imgs) < 30 {
		t.Fatalf("found %d <img> tags, expected the full gallery", len(imgs))
	}

	var eager []string
	for _, tag := range imgs {
		// The nav logo is above the fold and must stay eager; the lightbox
		// image has no src until JS sets one.
		if strings.Contains(tag, "nav-logo-img") || strings.Contains(tag, "lightbox-img") {
			continue
		}
		if !strings.Contains(tag, `loading="lazy"`) {
			eager = append(eager, tag)
		}
	}
	if len(eager) > 0 {
		t.Errorf("%d below-the-fold images load eagerly, e.g. %s", len(eager), eager[0])
	}
}
