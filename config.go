package main

import (
	"os"
	"strings"
)

// Config is every environment-driven setting the server reads, resolved once at
// startup so nothing has to mutate the environment to be constructed.
//
// SMTP settings are deliberately absent — handlers/contact.go reads those
// inline, and moving them here means threading config into package-level
// handler functions.
type Config struct {
	Port   string
	DBPath string

	// Public origin, no trailing slash, e.g. "https://prebuiltshedsllc.com".
	// Empty is supported, not a misconfiguration: local dev has no public
	// origin, and the templates omit canonical/OG tags entirely rather than
	// emitting ones that point nowhere, which is worse than none.
	SiteURL string

	// Compared in constant time by the Basic Auth validator. Empty means the
	// admin group fails closed rather than open.
	AdminUser string
	AdminPass string

	// URL prefix the admin group mounts at, leading slash and no trailing one.
	//
	// Configurable so the real path stays out of this public repository: the
	// default is the obvious guess, and production sets something else in .env.
	// This is noise reduction, not a security control — it keeps blind scanners
	// out of the logs, and the auth, rate limit, and CSRF are what actually stop
	// an attacker who finds the path.
	AdminPath string

	// Installs the X-Forwarded-For IP extractor. Required behind nginx, and
	// dangerous without it: a directly-exposed server would let a client spoof
	// its address and evade the per-IP rate limits.
	TrustProxy bool

	// Must be on in production and off on plain-HTTP local dev, or the browser
	// drops the cookie and admin logins break.
	CookieSecure bool

	// Switches the CSP to report-only. A CSP mistake fails silently and totally,
	// so this makes a bad policy a restart rather than a redeploy.
	CSPReportOnly bool

	// Registers the /instock routes. Off by default; when off the routes don't
	// exist at all rather than existing unlinked.
	FeatureInstock bool
}

// loadConfig reads the environment. It never fails: unset values take their
// documented defaults, and missing admin credentials fail closed at the auth
// check rather than refusing to boot — the homepage should still serve.
func loadConfig() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// SQLite writes its journal/WAL sidecars next to the database, so this must
	// be a path inside a mounted *directory*, never a bind-mounted file.
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./prebuilt.db"
	}

	return Config{
		Port:   port,
		DBPath: dbPath,
		// Trailing slash trimmed so templates concatenate paths without
		// producing "https://example.com//instock".
		SiteURL:        strings.TrimRight(os.Getenv("SITE_URL"), "/"),
		AdminUser:      os.Getenv("ADMIN_USER"),
		AdminPass:      os.Getenv("ADMIN_PASS"),
		AdminPath:      adminPath(os.Getenv("ADMIN_PATH")),
		TrustProxy:     envBool("TRUST_PROXY"),
		CookieSecure:   envBool("COOKIE_SECURE"),
		CSPReportOnly:  envBool("CSP_REPORT_ONLY"),
		FeatureInstock: envBool("FEATURE_INSTOCK"),
	}
}

// adminPath normalises ADMIN_PATH to a leading slash with no trailing one, so
// "marty", "/marty" and "/marty/" all mount the same group and the templates can
// concatenate without producing "//".
//
// An empty or slash-only value falls back to the default rather than mounting
// the admin panel at the site root, which would shadow the homepage.
func adminPath(raw string) string {
	p := "/" + strings.Trim(strings.TrimSpace(raw), "/")
	if p == "/" {
		return "/admin"
	}
	return p
}

// envBool treats only the exact string "true" as on. Strict on purpose: every
// unrecognised value means false, so there is one spelling to get right rather
// than a set that silently grows.
func envBool(key string) bool {
	return os.Getenv(key) == "true"
}
