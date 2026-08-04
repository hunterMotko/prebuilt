package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
)

var reqLog = log.New(os.Stdout, "", 0)

// classify turns a status code into a stable, greppable category. These strings
// are the intended search keys when investigating an incident.
func classify(status int) (category, level string) {
	switch {
	case status >= 500:
		return "server_error", "error"
	case status == http.StatusTooManyRequests:
		return "rate_limited", "warn" // abuse or a misconfigured client
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "auth_failed", "warn" // credential guessing against /admin
	case status == http.StatusRequestEntityTooLarge:
		return "payload_too_large", "warn"
	case status == http.StatusBadRequest:
		return "bad_request", "warn" // includes rejected CSRF tokens
	case status == http.StatusUnprocessableEntity:
		return "validation_failed", "info" // a real person mis-filling a form
	default:
		return "client_error", "info"
	}
}

type requestLogEntry struct {
	Time      string `json:"time"`
	Level     string `json:"level"`
	Category  string `json:"category"`
	Status    int    `json:"status"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	IP        string `json:"ip"`
	LatencyMS int64  `json:"latency_ms"`
	UserAgent string `json:"user_agent,omitempty"`
	Error     string `json:"error,omitempty"`
}

// errorOnlyLogger returns middleware that logs 4xx and 5xx responses as JSON
// lines on stdout, and nothing else.
//
// nginx sits in front of this app and already writes an access-log line for
// every request that reaches it, so duplicating that here would double the disk
// I/O to record the same facts. nginx is the system of record for raw traffic;
// this logger records only what nginx cannot see, which is why a request failed,
// plus the responses that indicate abuse.
//
// Deliberately not logged:
//   - Any 2xx or 3xx. nginx has them, and they are the overwhelming majority.
//   - 404. nginx has these too, and unmatched-route noise from bot scanners is
//     the bulk of it. Scanning is better handled where the data already lives,
//     since fail2ban reads nginx's access log directly.
//   - Request bodies and form values. They hold customer names, phone numbers,
//     and email addresses, which do not belong in a log file that no retention
//     policy covers.
func errorOnlyLogger() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			err := next(c)
			if err != nil {
				// Hand the error to the HTTP error handler now so the response
				// status below is the real one. Echo's own logger does the
				// same and then stops propagating, which is why this returns
				// nil afterwards — returning err too would handle it twice.
				c.Error(err)
			}

			status := c.Response().Status
			if status < 400 {
				return nil
			}
			if status == http.StatusNotFound {
				return nil
			}

			category, level := classify(status)
			entry := requestLogEntry{
				Time:      time.Now().UTC().Format(time.RFC3339),
				Level:     level,
				Category:  category,
				Status:    status,
				Method:    c.Request().Method,
				Path:      c.Request().URL.Path,
				IP:        c.RealIP(),
				LatencyMS: time.Since(start).Milliseconds(),
				UserAgent: c.Request().UserAgent(),
			}
			if err != nil {
				entry.Error = err.Error()
			}

			// Marshal first, then a single Print: log.Logger holds a mutex, so
			// concurrent requests can't interleave partial lines.
			if line, mErr := json.Marshal(entry); mErr == nil {
				reqLog.Print(string(line))
			}
			return nil
		}
	}
}
