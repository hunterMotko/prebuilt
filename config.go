package main

import (
	"os"
	"strings"
)

// Config is every environment-driven setting the server reads, resolved once at
// startup.
//
// Previously these were nine scattered os.Getenv calls buried in the middle of
// main(), which had two costs: there was no single place to see what the
// process is configured by, and nothing could be constructed without mutating
// the environment first — which is what made the wiring untestable.
//
// SMTP settings are deliberately absent. Those are read inside
// handlers/contact.go, and moving them here would mean threading config into
// package-level handler functions; that is a larger change (see DEBT-4) and not
// required for what this refactor is for.
type Config struct {
	Port   string
	DBPath string

	// SiteURL is the public origin, no trailing slash, e.g.
	// "https://prebuiltshedsllc.com". Required for canonical links and Open
	// Graph tags, both of which must carry absolute URLs — a relative og:image
	// is simply ignored by every scraper.
	//
	// Empty is a supported state, not a misconfiguration: local development has
	// no meaningful public origin, and the templates omit those tags entirely
	// rather than emitting a canonical that points nowhere. A wrong canonical
	// is considerably worse than no canonical — it tells search engines the
	// real page lives somewhere it does not.
	SiteURL string

	// AdminUser and AdminPass are compared in constant time by the Basic Auth
	// validator. Empty means /admin fails closed rather than open.
	AdminUser string
	AdminPass string

	// TrustProxy installs the X-Forwarded-For IP extractor. Required behind
	// nginx, and dangerous without it: on a directly-exposed server a client
	// could spoof its own address and evade the per-IP rate limits.
	TrustProxy bool

	// CookieSecure marks cookies Secure. Must be on in production, and must be
	// off on plain-HTTP local development or the browser drops the cookie
	// entirely and admin logins break.
	CookieSecure bool

	// CSPReportOnly switches the policy to report-only. A CSP mistake fails
	// silently and totally, so this is the lever that makes a bad policy a
	// restart rather than a rebuild and redeploy.
	CSPReportOnly bool

	// FeatureInstock registers the /instock routes. Off by default so an
	// unfinished feature cannot reach production by accident; when off the
	// routes do not exist at all, rather than existing unlinked where someone
	// could still reach them by guessing the URL.
	FeatureInstock bool
}

// loadConfig reads the environment. It never fails: an unset value takes its
// documented default, and the settings that must not default silently
// (ADMIN_USER, ADMIN_PASS) are enforced by failing closed at the auth check
// rather than by refusing to boot — a marketing site should still serve its
// homepage when only the admin credentials are missing.
func loadConfig() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// SQLite writes its journal/WAL sidecars next to the database, so this is
	// a path inside a mounted *directory*, never a bind-mounted file.
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./prebuilt.db"
	}

	return Config{
		Port:   port,
		DBPath: dbPath,
		// Trailing slash trimmed so templates can concatenate paths without
		// producing "https://example.com//instock".
		SiteURL:        strings.TrimRight(os.Getenv("SITE_URL"), "/"),
		AdminUser:      os.Getenv("ADMIN_USER"),
		AdminPass:      os.Getenv("ADMIN_PASS"),
		TrustProxy:     envBool("TRUST_PROXY"),
		CookieSecure:   envBool("COOKIE_SECURE"),
		CSPReportOnly:  envBool("CSP_REPORT_ONLY"),
		FeatureInstock: envBool("FEATURE_INSTOCK"),
	}
}

// envBool treats only the exact string "true" as on. Deliberately strict: a
// flag that also accepted "1", "yes", or "True" would make a typo like
// TRUST_PROXY=ture silently mean false, and for that particular flag the
// silent-false case is a security property (spoofable client IPs) rather than
// a cosmetic one.
func envBool(key string) bool {
	return os.Getenv(key) == "true"
}
