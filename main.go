package main

import (
	"context"
	"crypto/subtle"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/signal"
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

	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	// Standard security headers (nosniff, X-Frame-Options, XSS protection).
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:      "1; mode=block",
		ContentTypeNosniff: "nosniff",
		XFrameOptions:      "SAMEORIGIN",
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
	}))

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

	tmpl := template.Must(template.ParseGlob("templates/*.html"))
	tmpl = template.Must(tmpl.ParseGlob("templates/partials/*.html"))
	tmpl = template.Must(tmpl.ParseGlob("templates/admin/*.html"))
	e.Renderer = &TemplateRenderer{templates: tmpl}

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
	e.Match([]string{http.MethodGet, http.MethodHead}, "/instock", handlers.Instock)
	e.POST("/instock/interest", handlers.InstockInterest, publicBodyLimit, formRateLimit)

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

	admin := e.Group("/admin", adminAuth, middleware.BodyLimit("40M"), adminCSRF)
	admin.GET("", handlers.AdminList)
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
}
