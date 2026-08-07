package main

import (
	"crypto/subtle"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"github.com/hunterMotko/prebuilt/handlers"
)

// TemplateRenderer adapts a parsed html/template set to echo.Renderer.
type TemplateRenderer struct {
	templates *template.Template
}

// Render executes the named template into w. It satisfies echo.Renderer, whose
// signature carries an echo.Context this implementation does not need.
func (t *TemplateRenderer) Render(w io.Writer, name string, data any, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

// newServer builds the fully wired Echo instance: middleware, templates,
// routes, and timeouts. It starts nothing and reads no environment variables.
//
// Split out of main() so tests exercise the real wiring. Middleware ordering,
// the catch-all RouteNotFound a middleware-bearing Group auto-registers,
// admin.GET("") path joining, and the nested public-3M/admin-40M body limits
// are properties of this function alone — a test that built its own echo.New()
// would assert against a copy and stay green through the static-asset 404 that
// actually reached production.
func newServer(cfg Config) (*echo.Echo, error) {
	e := echo.New()
	e.HideBanner = true

	// Behind nginx every request arrives from the proxy's address, so without
	// this the per-IP rate limiters below see one client for the whole internet
	// and 429 real customers collectively. Off by default so a directly-exposed
	// server can't be fed a spoofed X-Forwarded-For.
	if cfg.TrustProxy {
		e.IPExtractor = echo.ExtractIPFromXFFHeader()
	}

	// Errors and abuse signals only — nginx is the access log. See logging.go
	// for exactly what is kept and why.
	e.Use(errorOnlyLogger())
	e.Use(middleware.Recover())
	// Standard security headers (nosniff, X-Frame-Options, XSS protection).
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		// "0", not "1; mode=block". The header enabled a legacy browser XSS
		// auditor that was itself exploitable; every browser has removed it. Set
		// explicitly only because Echo's default is the deprecated value.
		XSSProtection:      "0",
		ContentTypeNosniff: "nosniff",
		XFrameOptions:      "SAMEORIGIN",
		// Stops the full current URL travelling as the Referer on outbound
		// clicks. Harmless from the homepage; not from /admin/inventory/17/edit.
		ReferrerPolicy: "strict-origin-when-cross-origin",
		// Set explicitly because Echo's default HSTSMaxAge of 0 silently means
		// the header is never sent — plain middleware.Secure() looks like HSTS
		// coverage but isn't. Echo emits it on TLS or X-Forwarded-Proto: https,
		// so it stays dormant on local dev. Don't let nginx also send one.
		HSTSMaxAge: 31536000, // 1 year
		// includeSubdomains would force every future subdomain to be HTTPS-only
		// in any browser that has seen this header — a confusing outage if one
		// ever isn't. Flip once every subdomain is certain to have a cert.
		HSTSExcludeSubdomains: true,

		// Every asset is same-origin (htmx is vendored, no CDN/fonts/analytics),
		// so 'self' needs no host allowlist and no nonce — the two admin inline
		// blocks moved to public/js/admin.js.
		//
		//   img-src data:         select-arrow SVG embedded in style.css.
		//   style-src-attr        colour swatches render style="background:{{.Hex}}"
		//                         from owner-editable DB rows. Split out so
		//                         style-src stays strict: inline ATTRIBUTES are
		//                         allowed, an injected <style> block is not.
		//   base-uri/form-action  <base> injection and form hijacking.
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

		// A CSP mistake fails silently and totally — the browser blocks the
		// script and nothing is logged server-side, so carousels, mobile nav and
		// the contact form break with no signal here. Deploy with
		// CSP_REPORT_ONLY=true, click through with devtools open, then remove.
		// An env var so reverting is a restart, not a redeploy.
		CSPReportOnly: cfg.CSPReportOnly,
	}))

	// No field for this in Echo's SecureConfig. Nothing here uses the camera,
	// microphone or location, so denying them costs nothing and means an
	// injected script can't prompt a visitor under this domain's name.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Permissions-Policy",
				"geolocation=(), camera=(), microphone=(), payment=(), usb=(), interest-cohort=()")
			return next(c)
		}
	})

	// Nothing fingerprints these filenames, so without an explicit Cache-Control
	// a browser reuses a stale style.css after a deploy. "no-cache" means
	// "revalidate before reusing", not "don't cache" — an unchanged file still
	// returns a cheap 304.
	//
	// A path-checked global, NOT e.Group("/public", ...): attaching middleware to
	// a group makes Echo auto-register a catch-all RouteNotFound for that prefix,
	// which shadows the static route and 404s every asset. That shipped once.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if strings.HasPrefix(c.Request().URL.Path, "/public/") {
				c.Response().Header().Set("Cache-Control", "no-cache")
			}
			return next(c)
		}
	})

	// Set FEATURE_INSTOCK=true in .env to work on /instock locally.
	featureInstock := cfg.FeatureInstock

	// Re-normalised rather than trusted: loadConfig() already did this, but a
	// zero-value Config built in a test would leave it empty, and e.Group("")
	// mounts the whole admin panel at the site root behind the homepage.
	adminPrefix := adminPath(cfg.AdminPath)

	// Template funcs rather than per-handler data maps: nav.html and footer.html
	// are included by every page, so the data-map route means touching every
	// handler and remembering to do it again for each new one. Funcs must be
	// attached before parsing, hence template.New().Funcs() over ParseGlob.
	funcs := template.FuncMap{
		"featureInstock": func() bool { return featureInstock },
		// Render time, stamped into both public forms so the handler can reject a
		// submission that arrived faster than a person could type.
		"formTS": func() string { return strconv.FormatInt(time.Now().Unix(), 10) },
		// Canonical and Open Graph tags need absolute URLs. Returns "" when
		// unset and the templates omit the whole block — a canonical pointing
		// nowhere actively tells search engines the real page is elsewhere.
		"siteURL": func() string { return cfg.SiteURL },
		// Prefix for every link and htmx target in templates/admin/. Hardcoding
		// "/admin" there would pin the panel to one path and defeat ADMIN_PATH.
		"adminPath": func() string { return adminPrefix },
	}

	// Not template.Must: a glob matching nothing is what a bad Dockerfile COPY
	// produces, and an error naming the pattern beats a panic.
	tmpl := template.New("").Funcs(funcs)
	for _, pattern := range []string{
		"templates/*.html",
		"templates/partials/*.html",
		"templates/admin/*.html",
	} {
		var err error
		if tmpl, err = tmpl.ParseGlob(pattern); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", pattern, err)
		}
	}
	e.Renderer = &TemplateRenderer{templates: tmpl}

	// Echo's default handler emits JSON, so a mistyped URL rendered
	// {"message":"Not Found"} on a marketing site. Set after the renderer
	// because it renders error.html.
	e.HTTPErrorHandler = handlers.HTTPErrorHandler

	e.Static("/public", "public")

	// Generated, not served from disk: the sitemap has to track FEATURE_INSTOCK
	// and the deployed origin, neither of which a static file can. See
	// handlers/seo.go.
	e.GET("/robots.txt", handlers.Robots(cfg.SiteURL))
	e.GET("/sitemap.xml", handlers.Sitemap(cfg.SiteURL, featureInstock))

	// Scoped to the public text-only POSTs, not global: BodyLimit wraps the body
	// reader, so a stricter outer wrap can't be loosened by /admin's looser 40M
	// one below.
	publicBodyLimit := middleware.BodyLimit("3M")

	// Each public form POST writes to the DB and fires an email, so without a
	// limit one bot floods both. Burst of 5, then one every 5 seconds — far above
	// any real customer's rate.
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

	// Not registered when the flag is off, so /instock is a plain 404. Hiding
	// only the nav link would leave the page and its interest form publicly live
	// to anyone guessing the URL.
	if featureInstock {
		e.Match([]string{http.MethodGet, http.MethodHead}, "/instock", handlers.Instock)
		e.POST("/instock/interest", handlers.InstockInterest, publicBodyLimit, formRateLimit)
	}

	adminAuth := middleware.BasicAuth(func(username, password string, c echo.Context) (bool, error) {
		expectedUser := cfg.AdminUser
		expectedPass := cfg.AdminPass
		if expectedUser == "" || expectedPass == "" {
			return false, nil // fail closed if misconfigured
		}
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(expectedUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(expectedPass)) == 1
		return userOK && passOK, nil
	})

	// Basic Auth credentials auto-attach to same-origin requests whatever page
	// triggered them, so /admin needs CSRF on top of auth — otherwise a page an
	// admin merely has open could trigger real changes. TokenLookup covers both
	// plain form POSTs and htmx's header, wired via htmx:configRequest.
	adminCSRF := middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup:    "header:X-CSRF-Token,form:csrf",
		CookieHTTPOnly: true,
		CookieSameSite: http.SameSiteStrictMode,
		// Can't be hardcoded true: a Secure cookie is dropped entirely over
		// plain HTTP, which breaks local dev. Set COOKIE_SECURE=true on HTTPS.
		CookieSecure: cfg.CookieSecure,
		// Without an explicit path the cookie defaults to the directory of
		// whichever admin sub-path set it first, so a token from one page fails
		// on another.
		CookiePath: adminPrefix,
	})

	// Basic Auth has no attempt limit of its own. Ordered BEFORE adminAuth in the
	// group so rejected requests count too — behind it, this would only throttle
	// the legitimate admin. 20 through, then one every two seconds: ample for
	// clicking through inventory, and a guessing loop drops to 30/minute. Against
	// a 32-char password that's already hopeless, so the real wins are log noise
	// and off-the-shelf credential stuffing. fail2ban also bans the host.
	adminRateLimit := middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(0.5),
			Burst:     20,
			ExpiresIn: 10 * time.Minute,
		}),
	})

	// Mounted at cfg.AdminPath rather than a literal so the deployed path can
	// differ from the default this repository shows. The handlers need it too,
	// for their post-action redirects.
	handlers.SetAdminPath(adminPrefix)
	admin := e.Group(adminPrefix, adminRateLimit, adminAuth, middleware.BodyLimit("40M"), adminCSRF)
	admin.GET("", handlers.AdminList)
	admin.GET("/submissions", handlers.AdminSubmissions)
	admin.POST("/submissions/:id/status", handlers.AdminUpdateSubmissionLeadStatus)
	admin.DELETE("/submissions/:id", handlers.AdminDeleteSubmission)
	admin.GET("/inventory/new", handlers.AdminNewItemForm)
	admin.POST("/inventory", handlers.AdminCreateItem)
	admin.GET("/inventory/:id/edit", handlers.AdminEditItemForm)
	admin.POST("/inventory/:id", handlers.AdminUpdateItem)
	admin.POST("/inventory/:id/status", handlers.AdminUpdateStatus)
	admin.DELETE("/inventory/:id", handlers.AdminDeleteItem)
	admin.DELETE("/images/:imageId", handlers.AdminDeleteImage)

	// Timeouts so a client holding a connection open can't tie up the server
	// indefinitely (slowloris). ReadHeaderTimeout is the main defense; the
	// read/write timeouts are generous because admin photo uploads (up to
	// 40MB) legitimately take a while on slow connections.
	e.Server.ReadHeaderTimeout = 10 * time.Second
	e.Server.ReadTimeout = 5 * time.Minute
	e.Server.WriteTimeout = 5 * time.Minute
	e.Server.IdleTimeout = 2 * time.Minute

	return e, nil
}
