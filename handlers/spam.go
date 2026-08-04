package handlers

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// Bot defence for the two public forms.
//
// The per-IP rate limiter doesn't cover this: spam bots rotate IPs, and five
// hosts sending one submission each never trip it. Each one that lands looks
// exactly like a real lead, which is how an owner stops trusting the inbox the
// business runs on.
//
// Two cheap checks. The honeypot is the load-bearing one — a field a human never
// sees and a form-filler always fills, hidden with CSS rather than
// type="hidden" because bots skip hidden inputs but complete off-screen text
// ones. The submit-time floor only catches bots that POST the instant the page
// parses: the timestamp is client-controlled and deliberately unsigned, since
// signing it means introducing and rotating an application secret.
//
// Escalate to Turnstile/hCaptcha only if real spam gets through these.
const (
	// Named to be uninteresting to browser autofill. "company", "website" and
	// "organization" are recognised autocomplete tokens, and a honeypot Chrome
	// helpfully fills in would silently reject real customers.
	honeypotField = "contact_ref"
	formTimeField = "form_ts"

	// Generous on purpose, and with no upper bound — a customer who opens the
	// page, gets distracted, and submits the next morning must still get through.
	minFillSeconds = 3
)

// isBotSubmission reports whether a submission should be silently discarded.
//
// Biased hard toward false negatives. A wrongly-rejected submission shows the
// sender a success message and is then lost — the exact silent lead loss the
// email-status work exists to eliminate. Letting spam through costs a delete.
func isBotSubmission(c echo.Context) bool {
	if strings.TrimSpace(c.FormValue(honeypotField)) != "" {
		return true
	}

	// A missing or unparseable timestamp is NOT a bot signal: any page still in a
	// browser's back-forward cache from before this deployed has no form_ts, and
	// rejecting those drops real leads for as long as those tabs stay open.
	ts, err := strconv.ParseInt(c.FormValue(formTimeField), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < minFillSeconds*time.Second
}

var spamLog = log.New(os.Stdout, "", 0)

// logSpamRejection records the discard. Without this a false positive in the
// checks above is completely invisible — the caller returns the normal success
// page, so nothing else would ever show it happened.
//
// No form values are logged: they're customer names, phones and emails.
func logSpamRejection(c echo.Context, reason string) {
	entry := struct {
		Time      string `json:"time"`
		Level     string `json:"level"`
		Category  string `json:"category"`
		Reason    string `json:"reason"`
		Path      string `json:"path"`
		IP        string `json:"ip"`
		UserAgent string `json:"user_agent,omitempty"`
	}{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Level:     "warn",
		Category:  "spam_rejected",
		Reason:    reason,
		Path:      c.Request().URL.Path,
		IP:        c.RealIP(),
		UserAgent: c.Request().UserAgent(),
	}
	if line, err := json.Marshal(entry); err == nil {
		spamLog.Print(string(line))
	}
}

// spamReason names which check fired, for the log line only. Never returned to
// the client — telling a bot which trap it hit is how it learns to avoid it.
func spamReason(c echo.Context) string {
	if strings.TrimSpace(c.FormValue(honeypotField)) != "" {
		return "honeypot_filled"
	}
	return "submitted_too_fast"
}
