package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Home renders the marketing homepage. Its content is hardcoded in templates,
// so it reads nothing from the database and cannot fail.
func Home(c echo.Context) error {
	return c.Render(http.StatusOK, "index.html", map[string]interface{}{
		"Title": "Prebuilt Sheds LLC",
	})
}
