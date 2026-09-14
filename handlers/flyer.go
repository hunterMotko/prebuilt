package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// FlyerPage is one page of the printed flyer, pre-rendered to an image by
// scripts/render-flyer.sh.
type FlyerPage struct {
	Src string
	// The rendered pixel size. Emitted as the <img> width/height attributes so
	// the browser reserves the space before the image arrives, rather than the
	// page jumping as each one loads.
	Width, Height int
	Alt           string
}

// FlyerPages is the flyer, in page order. Update it whenever the script is
// re-run: the page count, and the dimensions the script prints.
//
// Exported so server.go can check at startup that every image exists. /flyer is
// what the QR codes on the printed flyer point at, so a missing image is found
// by a customer standing in the shop.
var FlyerPages = []FlyerPage{
	{
		Src: "/public/images/flyer-page-1.webp", Width: 1600, Height: 2071,
		Alt: "Flyer page 1: Standard Barn and Deluxe Barn & Gable price lists by size, " +
			"with photos of barn sheds, a porch shed, a 14x28 deluxe gable garage, " +
			"and a 16x36 deluxe gambrel barn.",
	},
	{
		Src: "/public/images/flyer-page-2.webp", Width: 1600, Height: 1241,
		Alt: "Flyer page 2: popular options and add-on prices, including roll-up " +
			"garage doors, entry doors, windows, skylights, shutters, work benches, " +
			"lofts, and ramps. Call Martin at (989) 387-9233 for an appointment.",
	},
}

// Flyer renders the flyer as a normal site page — nav, the flyer pages as
// images, a quote/call prompt, footer. The PDF itself is not served anywhere;
// see scripts/render-flyer.sh for why images rather than an embed.
func Flyer(c echo.Context) error {
	return c.Render(http.StatusOK, "flyer.html", map[string]any{
		"Title": "Shed Options Flyer — Prebuilt Sheds LLC",
		"Pages": FlyerPages,
	})
}
