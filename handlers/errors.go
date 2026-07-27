package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// publicMessage maps a status code to something a customer can act on. Internal
// error text is never surfaced — it leaks implementation detail and means
// nothing to the reader.
func publicMessage(code int) string {
	switch code {
	case http.StatusNotFound:
		return "That page doesn't exist. It may have moved, or the link may be out of date."
	case http.StatusTooManyRequests:
		return "That's a few too many requests in a row. Wait a moment and try again, or call us directly."
	case http.StatusRequestEntityTooLarge:
		return "That upload is too large."
	case http.StatusForbidden, http.StatusUnauthorized:
		return "You don't have access to that."
	case http.StatusBadRequest:
		return "That request couldn't be processed. Refresh the page and try again."
	default:
		return "Something went wrong on our end. Please try again, or call us directly."
	}
}

func statusTitle(code int) string {
	if code == http.StatusNotFound {
		return "Page Not Found"
	}
	return "Something Went Wrong"
}

// HTTPErrorHandler replaces Echo's default, which emits JSON. On a public
// marketing site that meant a mistyped URL rendered {"message":"Not Found"},
// and a rate-limited visitor got {"message":"rate limit exceeded"} swapped
// straight into the page where the form had been.
func HTTPErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	code := http.StatusInternalServerError
	if he, ok := err.(*echo.HTTPError); ok {
		code = he.Code
	}

	// htmx swaps the response body into the DOM, so an error response has to be
	// an HTML fragment shaped like the element it replaces — not a page, and
	// certainly not JSON. errorHTML carries id="form-response" so the swap
	// target survives and the form keeps working afterwards.
	if c.Request().Header.Get("HX-Request") == "true" {
		if htmlErr := c.HTML(code, errorHTML(publicMessage(code))); htmlErr != nil {
			c.Logger().Error("error fragment render failed:", htmlErr)
		}
		return
	}

	// HEAD must not carry a body; uptime monitors probe with it.
	if c.Request().Method == http.MethodHead {
		if headErr := c.NoContent(code); headErr != nil {
			c.Logger().Error("error NoContent failed:", headErr)
		}
		return
	}

	if renderErr := c.Render(code, "error.html", map[string]any{
		"Title":   statusTitle(code) + " — Prebuilt Sheds LLC",
		"Code":    code,
		"Heading": statusTitle(code),
		"Message": publicMessage(code),
	}); renderErr != nil {
		// Falling back to plain text rather than recursing into the error
		// handler, which would loop.
		c.Logger().Error("error page render failed:", renderErr)
		if strErr := c.String(code, publicMessage(code)); strErr != nil {
			c.Logger().Error("error string fallback failed:", strErr)
		}
	}
}
