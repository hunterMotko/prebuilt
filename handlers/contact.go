package handlers

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

// Contact handles POST /contact, the public lead form. It returns the
// contact_success.html fragment for htmx to swap in, or an error fragment.
//
// The submission is saved before it is mailed, and the send happens in a
// goroutine, so a broken or unconfigured SMTP server costs the delivery but
// never the lead. A bot submission is answered with the ordinary success page so
// it cannot learn it was filtered.
func Contact(c echo.Context) error {
	// Checked before validation so a bot gets no feedback at all — not even
	// "you missed a required field". The response is the ordinary success page
	// so it cannot tell it was filtered. See handlers/spam.go.
	if isBotSubmission(c) {
		logSpamRejection(c, spamReason(c))
		return c.Render(http.StatusOK, "contact_success.html", nil)
	}

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

	id, err := database.SaveContactSubmission(sub)
	if err != nil {
		c.Logger().Error("db save failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again or call us directly."))
	}

	queueEmail(id, sub)

	return c.Render(http.StatusOK, "contact_success.html", nil)
}

// emailWG tracks in-flight sends so shutdown can wait for them. Without it a
// deploy landing between "row saved" and "mail sent" kills the goroutine
// mid-flight and the notification is lost with no trace.
var emailWG sync.WaitGroup

// queueEmail sends the notification in the background and records the outcome
// against the submission, so a failure is durable rather than a log line that
// rotates away. The customer is never made to wait on SMTP.
func queueEmail(id int64, sub database.ContactSubmission) {
	emailWG.Add(1)
	go func() {
		defer emailWG.Done()

		status, sendErr := sendEmail(sub)
		msg := ""
		if sendErr != nil {
			msg = sendErr.Error()
			log.Printf("email delivery failed for submission %d: %v", id, sendErr)
		}
		if err := database.MarkEmailStatus(id, status, msg); err != nil {
			log.Printf("could not record email status for submission %d: %v", id, err)
		}
	}()
}

// WaitForEmails blocks until in-flight sends finish or ctx expires. Called
// during graceful shutdown.
func WaitForEmails(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		emailWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		log.Print("shutdown timeout reached with email sends still in flight")
	}
}

// sendEmail attempts delivery and reports what happened. A nil error with
// EmailSkipped means no attempt was made because SMTP isn't configured — a
// distinct outcome from a failed send, and one that previously returned
// silently so nobody could tell email was switched off entirely.
func sendEmail(sub database.ContactSubmission) (string, error) {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	to := os.Getenv("CONTACT_EMAIL")

	if host == "" || user == "" || to == "" {
		return database.EmailSkipped, nil
	}

	if port == "" {
		port = "587"
	}

	// Explicit charset so non-ASCII names (e.g. "Zoë") render correctly rather
	// than as mojibake, and a Date header because its absence is a mild spam
	// signal. User input stays strictly in the body, after the blank line.
	body := fmt.Sprintf(
		"Subject: New Contact Form Submission - Prebuilt Sheds LLC\r\n"+
			"From: %s\r\nTo: %s\r\nDate: %s\r\n"+
			"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+
			"Name: %s\nPhone: %s\nEmail: %s\nStyle: %s\nSize: %s\n\nDetails:\n%s",
		user, to, time.Now().Format(time.RFC1123Z),
		sub.Name, sub.Phone, sub.Email, sub.Style, sub.Size, sub.Details,
	)

	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtp.SendMail(host+":"+port, auth, user, []string{to}, []byte(body)); err != nil {
		return database.EmailFailed, err
	}
	return database.EmailSent, nil
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
// The id matters: the public forms use hx-target="#form-response" with
// hx-swap="outerHTML", so this fragment REPLACES that element. Without the id
// the target is destroyed by the first error, and the next submit has nowhere
// to swap into — the form silently stops responding. contact_success.html
// carries the same id for the same reason.
func errorHTML(msg string) string {
	return fmt.Sprintf(
		`<div class="form-error" id="form-response"><p>%s</p></div>`,
		html.EscapeString(msg),
	)
}
