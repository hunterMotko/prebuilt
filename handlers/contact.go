package handlers

import (
	"fmt"
	"html"
	"net/http"
	"net/smtp"
	"os"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

func Contact(c echo.Context) error {
	sub := database.ContactSubmission{
		Name:    strings.TrimSpace(c.FormValue("name")),
		Phone:   strings.TrimSpace(c.FormValue("phone")),
		Email:   strings.TrimSpace(c.FormValue("email")),
		Style:   c.FormValue("style"),
		Size:    strings.TrimSpace(c.FormValue("size")),
		Details: strings.TrimSpace(c.FormValue("details")),
	}

	if sub.Name == "" || sub.Phone == "" || sub.Email == "" || sub.Style == "" || sub.Size == "" {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Please fill in all required fields."))
	}
	if tooLong(shortFieldMax, sub.Name, sub.Phone, sub.Email, sub.Style, sub.Size) || tooLong(longFieldMax, sub.Details) {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("One of the fields is too long."))
	}

	if _, err := database.SaveContactSubmission(sub); err != nil {
		c.Logger().Error("db save failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again or call us directly."))
	}

	go sendEmail(sub)

	return c.Render(http.StatusOK, "contact_success.html", nil)
}

func sendEmail(sub database.ContactSubmission) {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	to := os.Getenv("CONTACT_EMAIL")

	if host == "" || user == "" || to == "" {
		return
	}

	if port == "" {
		port = "587"
	}

	body := fmt.Sprintf(
		"Subject: New Contact Form Submission - Prebuilt Sheds LLC\r\nFrom: %s\r\nTo: %s\r\n\r\n"+
			"Name: %s\nPhone: %s\nEmail: %s\nStyle: %s\nSize: %s\n\nDetails:\n%s",
		user, to, sub.Name, sub.Phone, sub.Email, sub.Style, sub.Size, sub.Details,
	)
	auth := smtp.PlainAuth("", user, pass, host)
	_ = smtp.SendMail(host+":"+port, auth, user, []string{to}, []byte(body))
}

// The public form inputs carry maxlength attributes, but those are
// client-side only — these are the server-side backstops. Byte counts, sized
// generously above the client limits so multi-byte characters in legitimate
// input never trip them; their job is stopping megabytes of bot filler, not
// enforcing exact UI limits.
const (
	shortFieldMax = 500
	longFieldMax  = 10_000
)

func tooLong(max int, values ...string) bool {
	for _, v := range values {
		if len(v) > max {
			return true
		}
	}
	return false
}

// errorHTML always escapes msg, even though most call sites pass a hardcoded
// literal — a couple build the message from request data (e.g. an uploaded
// filename), and escaping here means every current and future call site is
// safe by construction rather than relying on each one to remember to do it.
func errorHTML(msg string) string {
	return fmt.Sprintf(`<div class="form-error"><p>%s</p></div>`, html.EscapeString(msg))
}
