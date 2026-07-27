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
// The per-IP rate limiter in main.go stops a single host hammering the
// endpoint, but spam bots rotate IPs — five hosts sending one submission each
// never come close to tripping it. Each one that gets through becomes a
// contact_submissions row and a notification email that looks exactly like a
// real lead, which is how an owner stops trusting the inbox the business runs
// on. Rate limiting alone does not address that.
//
// Two cheap checks, in order of how much they actually catch:
//
//  1. Honeypot — a field a human never sees and a form-filler always fills.
//     Hidden with CSS rather than type="hidden": bots skip hidden inputs but
//     happily complete a text input that has been moved off-screen.
//  2. Submit-time floor — the render timestamp travels with the form, and a
//     submission that arrives implausibly fast was not typed by a person.
//
// The timestamp is deliberately NOT signed. It is client-controlled, so a bot
// that bothers to look can send whatever value it likes; this catches the
// naive "POST the instant the page parses" case and nothing more. Signing it
// would mean introducing and rotating an application secret, which is not
// proportionate to form spam on a shed site. The honeypot is the load-bearing
// check of the two.
//
// Escalate to Turnstile/hCaptcha only if real spam gets through these.
const (
	// Named to be uninteresting to browser autofill. "company", "website" and
	// "organization" are all recognised autocomplete tokens, and a honeypot
	// that Chrome helpfully fills in would silently reject real customers.
	honeypotField = "contact_ref"
	formTimeField = "form_ts"

	// Generous on purpose. The quote form has six fields and the interest form
	// four; nobody completes either in under three seconds. There is no upper
	// bound — a customer who opens the page, gets distracted, and submits the
	// next morning must still get through.
	minFillSeconds = 3
)

// isBotSubmission reports whether a submission should be silently discarded.
//
// Biased hard toward false negatives. A wrongly-rejected submission is the
// worst outcome this codebase has: the sender sees a success message and never
// hears back, which is precisely the silent lead loss the email-status work was
// done to eliminate. Letting some spam through costs the owner a delete.
func isBotSubmission(c echo.Context) bool {
	if strings.TrimSpace(c.FormValue(honeypotField)) != "" {
		return true
	}

	// A missing or unparseable timestamp is NOT treated as a bot signal. Any
	// page still held in a browser's back-forward cache from before this
	// deployed has no form_ts, and rejecting those would drop real leads for
	// as long as those tabs stay open. The check only fires on a timestamp
	// that parses and is too recent.
	ts, err := strconv.ParseInt(c.FormValue(formTimeField), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < minFillSeconds*time.Second
}

var spamLog = log.New(os.Stdout, "", 0)

// logSpamRejection records the discard. Suspicious traffic is one of the two
// things logging.go commits to keeping, and without a line here a
// false-positive in the checks above would be completely invisible — the
// caller returns the normal success page, so nothing else in the system would
// ever show it happened.
//
// No form values are logged, for the same reason logging.go gives: they are
// customer names, phone numbers and email addresses.
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
