package main

import (
	"context"
	"crypto/subtle"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"github.com/hunterMotko/prebuilt/database"
	"github.com/hunterMotko/prebuilt/handlers"
)

type TemplateRenderer struct {
	templates *template.Template
}

func (t *TemplateRenderer) Render(w io.Writer, name string, data any, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

func main() {
	_ = godotenv.Load()

	database.Init()

	e := echo.New()
	e.HideBanner = true

	// Behind a reverse proxy (nginx on the VPS) every request arrives from
	// the proxy's own address rather than the visitor's. Without
	// this the per-IP form rate limiter below would see a single client for
	// the entire internet and start 429-ing real customers collectively, so
	// this is required — not optional — whenever a proxy is in front.
	// Off by default so running directly on the internet can't be tricked by
	// a spoofed X-Forwarded-For header.
	if os.Getenv("TRUST_PROXY") == "true" {
		e.IPExtractor = echo.ExtractIPFromXFFHeader()
	}

	// Errors and abuse signals only — nginx is the access log. See logging.go
	// for exactly what is kept and why.
	e.Use(errorOnlyLogger())
	e.Use(middleware.Recover())
	// Standard security headers (nosniff, X-Frame-Options, XSS protection).
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		// "0", not "1; mode=block". The header switched on a legacy browser XSS
		// auditor that was itself a source of vulnerabilities — it could be
		// coaxed into leaking information by selectively blocking parts of a
		// page. Every browser that implemented it has since removed it, so this
		// changes nothing at runtime; it is set explicitly rather than omitted
		// because Echo's default is the deprecated value.
		XSSProtection:      "0",
		ContentTypeNosniff: "nosniff",
		XFrameOptions:      "SAMEORIGIN",
		// Without this the browser sends the full URL of the current page as
		// the Referer on outbound links. Harmless from the homepage, not
		// harmless from /admin/inventory/17/edit — that path would leak to
		// whatever third party an admin clicked through to. Cross-origin
		// requests now carry only the origin; same-site keeps the full path.
		ReferrerPolicy: "strict-origin-when-cross-origin",
		// Set explicitly because Echo's DefaultSecureConfig leaves HSTSMaxAge
		// at 0, which silently means the header is never sent at all — using
		// plain middleware.Secure() looks like HSTS coverage but isn't.
		// Echo emits it when the request is TLS *or* carries
		// X-Forwarded-Proto: https, so it stays dormant on plain-HTTP local
		// dev and switches on behind nginx once that header is forwarded.
		// Keep nginx from adding its own Strict-Transport-Security or the
		// response carries two.
		HSTSMaxAge: 31536000, // 1 year
		// Conservative on purpose: includeSubdomains would force every future
		// subdomain to be HTTPS-only in any browser that has seen this header,
		// which is a confusing outage to debug if one ever isn't. Flip to
		// false once you know every subdomain will have a certificate.
		HSTSExcludeSubdomains: true,

		// Content-Security-Policy. Every asset this site loads is served from
		// its own origin — htmx is vendored at /public/js/htmx.min.js, there are
		// no CDNs, web fonts, or analytics — so 'self' is genuinely sufficient
		// and no host allowlist is needed.
		//
		// No nonce. A nonce means generating a random value per request and
		// threading it into every template; it is the right answer when inline
		// scripts are unavoidable, and here they weren't. The two admin inline
		// blocks moved to public/js/admin.js instead, which is why script-src
		// can be a plain 'self' with no 'unsafe-inline' anywhere.
		//
		//   img-src data:      style.css embeds the select-arrow chevron as an
		//                      inline SVG data URI.
		//   style-src-attr     the admin and /instock colour swatches render as
		//                      style="background:{{.Hex}}" from owner-editable
		//                      DB rows, so those attributes must be allowed.
		//                      Split out deliberately: style-src itself stays
		//                      strict, so this permits inline style ATTRIBUTES
		//                      without permitting an injected <style> block.
		//                      html/template already applies CSS-context
		//                      escaping inside a style attribute.
		//   base-uri/form-action  block <base> injection and form hijacking —
		//                      the two things an attacker reaches for when
		//                      script injection is closed off.
		//   object-src 'none'  no plugins, ever.
		ContentSecurityPolicy: "default-src 'self'; " +
			"script-src 'self'; " +
			"style-src 'self'; " +
			"style-src-attr 'unsafe-inline'; " +
			"img-src 'self' data:; " +
			"font-src 'self'; " +
			"connect-src 'self'; " +
			"form-action 'self'; " +
			"base-uri 'self'; " +
			"frame-ancestors 'self'; " +
			"object-src 'none'",

		// A CSP mistake fails silently and totally: the browser refuses to run
		// the blocked script and the server logs nothing, so the carousels,
		// mobile nav, and contact form would break with no signal here. Deploy
		// with CSP_REPORT_ONLY=true, click through every page with devtools
		// open, then remove it. Kept as an env var rather than a code constant
		// so reverting is a restart, not a rebuild and redeploy.
		CSPReportOnly: os.Getenv("CSP_REPORT_ONLY") == "true",
	}))

	// Permissions-Policy has no field in Echo's SecureConfig, so it is set
	// here. It denies the powerful browser APIs outright — nothing on this site
	// uses the camera, microphone, or location, so an empty allowlist for each
	// costs nothing and means an injected script cannot prompt a visitor for
	// them under this domain's name. Defence in depth behind the CSP, not a
	// substitute for it.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Permissions-Policy",
				"geolocation=(), camera=(), microphone=(), payment=(), usb=(), interest-cohort=()")
			return next(c)
		}
	})

	// Static assets are served straight off disk and their URLs never change
	// (no build step fingerprints filenames), so without an explicit
	// Cache-Control a browser can keep reusing a stale style.css or main.js
	// after an edit or a deploy. "no-cache" doesn't mean "don't cache" — it
	// means "revalidate before reusing", so an unchanged file still comes back
	// as a cheap 304 from the Last-Modified check rather than a re-download.
	//
	// Applied as a path-checked global rather than via e.Group("/public", ...):
	// attaching middleware to a group makes Echo auto-register a catch-all
	// RouteNotFound for that prefix, which shadows the static route and 404s
	// every asset.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if strings.HasPrefix(c.Request().URL.Path, "/public/") {
				c.Response().Header().Set("Cache-Control", "no-cache")
			}
			return next(c)
		}
	})

	// ─── Feature flags ────────────────────────────────────────────────────────
	// Off unless explicitly switched on, so an unfinished feature can't reach
	// production by accident — enabling it is a deliberate, per-environment
	// act. Set FEATURE_INSTOCK=true in .env to work on /instock locally.
	featureInstock := os.Getenv("FEATURE_INSTOCK") == "true"

	// Exposed to templates as a function rather than threaded through each
	// handler's data map: nav.html and footer.html are included by every page,
	// so the data-map route would mean touching every handler and remembering
	// to do it again for each new one. Funcs have to be attached before
	// parsing, which is why this uses template.New(...).Funcs(...) instead of
	// the package-level template.ParseGlob.
	funcs := template.FuncMap{
		"featureInstock": func() bool { return featureInstock },
		// Render time, stamped into both public forms so the handler can reject
		// a submission that arrived faster than a person could type. A template
		// func rather than handler data for the same reason as featureInstock:
		// the contact form is a partial of the homepage, so threading it through
		// would mean editing every handler that renders a page containing a form.
		"formTS": func() string { return strconv.FormatInt(time.Now().Unix(), 10) },
	}

	tmpl := template.Must(template.New("").Funcs(funcs).ParseGlob("templates/*.html"))
	tmpl = template.Must(tmpl.ParseGlob("templates/partials/*.html"))
	tmpl = template.Must(tmpl.ParseGlob("templates/admin/*.html"))
	e.Renderer = &TemplateRenderer{templates: tmpl}

	// Echo's default handler emits JSON, so a mistyped URL rendered
	// {"message":"Not Found"} on a marketing site and a rate-limited visitor
	// got raw JSON swapped into the page by htmx. Set after the renderer
	// because it renders error.html.
	e.HTTPErrorHandler = handlers.HTTPErrorHandler

	e.Static("/public", "public")
	e.File("/robots.txt", "public/robots.txt")
	e.File("/sitemap.xml", "public/sitemap.xml")

	// A global BodyLimit would also constrain /admin's own (larger, for photo
	// uploads) limit below — BodyLimit wraps the body reader, so an outer
	// stricter wrap can't be loosened by an inner looser one. Scoping this to
	// just the public text-only POST routes avoids that conflict.
	publicBodyLimit := middleware.BodyLimit("3M")

	// Each public form POST writes to the DB and fires an email, so without a
	// limit one bot can flood both. Per-IP token bucket: a burst of 5, then
	// one request every 5 seconds — far above any real customer's rate.
	// Behind nginx this only counts real client IPs because TRUST_PROXY above
	// installs the X-Forwarded-For extractor; without that every visitor would
	// share one bucket.
	formRateLimit := middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(0.2),
			Burst:     5,
			ExpiresIn: 10 * time.Minute,
		}),
	})

	// Registered for HEAD as well as GET: uptime monitors commonly probe with
	// HEAD, and a GET-only route answers those with a 405 that reads as an
	// outage. Echo/net-http handles the empty-body part of HEAD itself.
	e.Match([]string{http.MethodGet, http.MethodHead}, "/", handlers.Home)
	e.POST("/contact", handlers.Contact, publicBodyLimit, formRateLimit)

	// Not registered at all when the flag is off, so /instock returns a plain
	// 404 rather than existing as an unlinked page someone could still reach
	// by guessing the URL or following a stale link. Hiding only the nav link
	// would leave the page — and its interest form — publicly live.
	if featureInstock {
		e.Match([]string{http.MethodGet, http.MethodHead}, "/instock", handlers.Instock)
		e.POST("/instock/interest", handlers.InstockInterest, publicBodyLimit, formRateLimit)
	}

	adminAuth := middleware.BasicAuth(func(username, password string, c echo.Context) (bool, error) {
		expectedUser := os.Getenv("ADMIN_USER")
		expectedPass := os.Getenv("ADMIN_PASS")
		if expectedUser == "" || expectedPass == "" {
			return false, nil // fail closed if misconfigured
		}
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(expectedUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(expectedPass)) == 1
		return userOK && passOK, nil
	})

	// Basic Auth credentials auto-attach to same-origin requests regardless of
	// which page triggered them, so /admin needs its own CSRF check on top of
	// auth — otherwise a page an admin merely has open could silently trigger
	// real changes. TokenLookup checks both a form field (plain POSTs from
	// admin_new.html/admin_edit.html) and a header (htmx requests, wired via
	// the htmx:configRequest listener in those same templates).
	adminCSRF := middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup:    "header:X-CSRF-Token,form:csrf",
		CookieHTTPOnly: true,
		// The admin CSRF cookie is never needed on a cross-site navigation,
		// so Strict costs nothing here and refuses to send it on any request
		// a third-party page initiates.
		CookieSameSite: http.SameSiteStrictMode,
		// Must be on in production so the cookie is never sent over plain
		// HTTP, but it can't be hardcoded: a Secure cookie is dropped
		// entirely on a plain-HTTP connection, which would break local dev.
		// Set COOKIE_SECURE=true wherever the site is served over HTTPS.
		CookieSecure: os.Getenv("COOKIE_SECURE") == "true",
		// Without an explicit path, the cookie defaults to the directory of
		// whichever admin sub-path first sets it (e.g. /admin/inventory/),
		// so a token picked up on one admin page can silently fail on
		// another. Scoping it to all of /admin keeps one shared cookie.
		CookiePath: "/admin",
	})

	// Basic Auth has no attempt limit of its own: without this an attacker can
	// pipeline credential guesses at whatever rate the server will answer, and
	// every one of them is a fresh bcrypt-free string comparison. Ordered
	// BEFORE adminAuth in the group so rejected requests are counted too —
	// behind the auth check it would only ever throttle the legitimate admin.
	//
	// Sized for a person, not a script: 20 straight through, then one every two
	// seconds. Clicking through inventory and uploading photos stays well
	// under that, while a guessing loop drops to 30 attempts a minute. Against
	// the current 32-character password that rate is already hopeless, so the
	// real wins are capping log noise and stopping off-the-shelf credential
	// stuffing. SEC-8 (fail2ban) is the version of this that also bans the
	// host, and covers SSH at the same time.
	adminRateLimit := middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(0.5),
			Burst:     20,
			ExpiresIn: 10 * time.Minute,
		}),
	})

	admin := e.Group("/admin", adminRateLimit, adminAuth, middleware.BodyLimit("40M"), adminCSRF)
	admin.GET("", handlers.AdminList)
	admin.GET("/submissions", handlers.AdminSubmissions)
	admin.GET("/inventory/new", handlers.AdminNewItemForm)
	admin.POST("/inventory", handlers.AdminCreateItem)
	admin.GET("/inventory/:id/edit", handlers.AdminEditItemForm)
	admin.POST("/inventory/:id", handlers.AdminUpdateItem)
	admin.POST("/inventory/:id/status", handlers.AdminUpdateStatus)
	admin.DELETE("/inventory/:id", handlers.AdminDeleteItem)
	admin.DELETE("/images/:imageId", handlers.AdminDeleteImage)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Timeouts so a client holding a connection open can't tie up the server
	// indefinitely (slowloris). ReadHeaderTimeout is the main defense; the
	// read/write timeouts are generous because admin photo uploads (up to
	// 40MB) legitimately take a while on slow connections.
	e.Server.ReadHeaderTimeout = 10 * time.Second
	e.Server.ReadTimeout = 5 * time.Minute
	e.Server.WriteTimeout = 5 * time.Minute
	e.Server.IdleTimeout = 2 * time.Minute

	// Graceful shutdown: on SIGINT/SIGTERM (Ctrl-C, systemd stop, deploy
	// restart), stop accepting new connections and give in-flight requests up
	// to 10 seconds to finish instead of dropping them mid-response.
	go func() {
		if err := e.Start(":" + port); err != nil && err != http.ErrServerClosed {
			e.Logger.Fatal("server error: ", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		e.Logger.Fatal(err)
	}

	// Notification emails are sent in background goroutines that Shutdown
	// knows nothing about, so without this a deploy landing between "lead
	// saved" and "mail sent" would drop the notification silently.
	handlers.WaitForEmails(ctx)
}
