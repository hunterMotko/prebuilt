package handlers

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// robots.txt and sitemap.xml used to be static files under public/. Two
// problems with that, both of which this file exists to fix:
//
//  1. The sitemap's /instock entry had to be commented out by hand while
//     FEATURE_INSTOCK was off, and uncommented by hand when it goes on. A
//     sitemap advertising a URL that 404s is a Search Console error, and one
//     that omits a live page is a page Google may never crawl. Coupling the
//     sitemap to the same flag that registers the route removes the
//     opportunity to forget.
//
//  2. Both files hardcoded https://prebuiltshedsllc.com, so a staging deploy
//     would hand Google the production domain.
//
// Serving them from public/ also left them reachable twice — /sitemap.xml and
// /public/sitemap.xml — which is a duplicate-content signal for free.

// origin resolves the absolute base URL these documents must contain. Both
// formats require absolute URLs; there is no relative form.
//
// SITE_URL wins when set. The fallback to the request's own host is what keeps
// dev and CI working without configuration, and is safe in a way that the
// canonical/og:url tags are NOT — which is why those are gated on SITE_URL
// instead of using this. The difference is who reads the output: a poisoned
// canonical tag is embedded in a page that real visitors and crawlers fetch, so
// a forged Host header there misdirects other people's traffic. A sitemap is
// fetched directly by the crawler, which sends the real Host, so a forged one
// only ever returns a bogus document to the person who forged it.
func origin(c echo.Context, siteURL string) string {
	if siteURL != "" {
		return siteURL
	}
	return c.Scheme() + "://" + c.Request().Host
}

// Robots serves /robots.txt.
//
// Disallow is "/admin", not "/admin/". robots.txt matching is a plain prefix
// match, so the trailing slash would have left /admin itself crawlable — the
// login prompt, which is the one URL worth keeping out of an index.
func Robots(siteURL string) echo.HandlerFunc {
	return func(c echo.Context) error {
		var b strings.Builder
		b.WriteString("User-agent: *\n")
		b.WriteString("Allow: /\n")
		b.WriteString("Disallow: /admin\n\n")
		fmt.Fprintf(&b, "Sitemap: %s/sitemap.xml\n", origin(c, siteURL))
		return c.String(http.StatusOK, b.String())
	}
}

type sitemapURL struct {
	Loc        string `xml:"loc"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// Sitemap serves /sitemap.xml, built from the routes actually registered.
//
// featureInstock is the same value newServer() uses to decide whether to
// register the /instock route, passed in rather than re-read, so the two cannot
// disagree.
//
// Marshalled rather than string-built: <loc> holds a host that may come from a
// request header, and the encoder escapes it. A hand-built string would emit an
// "&" straight into the document and produce a sitemap no parser accepts.
//
// No <lastmod>. The honest value would have to come from content change times
// this server does not track, and a lastmod that is always "now" teaches
// crawlers to ignore the field — worse than omitting it.
func Sitemap(siteURL string, featureInstock bool) echo.HandlerFunc {
	return func(c echo.Context) error {
		base := origin(c, siteURL)

		set := sitemapURLSet{
			NS: "http://www.sitemaps.org/schemas/sitemap/0.9",
			URLs: []sitemapURL{
				{Loc: base + "/", ChangeFreq: "weekly", Priority: "1.0"},
			},
		}
		if featureInstock {
			// Daily: inventory turns over, and a stale listing of a sold shed
			// is the one thing on this site that goes wrong by sitting still.
			set.URLs = append(set.URLs, sitemapURL{
				Loc: base + "/instock", ChangeFreq: "daily", Priority: "0.9",
			})
		}

		out, err := xml.MarshalIndent(set, "", "  ")
		if err != nil {
			return err
		}
		return c.Blob(http.StatusOK, "application/xml; charset=utf-8",
			append([]byte(xml.Header), append(out, '\n')...))
	}
}
