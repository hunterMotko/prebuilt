package handlers

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// origin resolves the absolute base URL robots.txt and sitemap.xml must
// contain; neither format has a relative form.
//
// SITE_URL wins when set; the Host fallback keeps dev and CI working with no
// configuration. Safe here in a way it would NOT be for canonical/og:url, which
// stay gated on SITE_URL — a forged Host on a canonical tag misdirects other
// people's traffic, because the tag is embedded in a page real visitors fetch. A
// sitemap is fetched by the crawler, which sends the true Host, so forging it
// only returns a bogus document to whoever forged it.
func origin(c echo.Context, siteURL string) string {
	if siteURL != "" {
		return siteURL
	}
	return c.Scheme() + "://" + c.Request().Host
}

// Robots serves /robots.txt, generated rather than kept as a static file so the
// Sitemap line always carries the origin the request actually arrived on.
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
// Generating it is what fixed three problems the static file had: the /instock
// entry needed commenting out by hand whenever FEATURE_INSTOCK was off, and a
// sitemap advertising a 404 is a Search Console error while one omitting a live
// page may never get crawled; the production domain was hardcoded; and the file
// was reachable at both /sitemap.xml and /public/sitemap.xml, which is a free
// duplicate-content signal.
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
