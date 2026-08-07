package handlers

import (
	"net/http"
	"net/url"
	"testing"
)

// These cases all reject before any database access, so no DB fixture is
// needed — the same property that makes a garbage request cheap in production.

func TestAdminUpdateSubmissionLeadStatusRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		status string
	}{
		{"non-numeric id", "abc", "confirmed"},
		{"empty status", "1", ""},
		{"unknown status", "1", "bogus"},
		{"email status not a lead status", "1", "sent"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFormContext(url.Values{"status": {tc.status}})
			c.SetParamNames("id")
			c.SetParamValues(tc.id)

			if err := AdminUpdateSubmissionLeadStatus(c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := c.Response().Status; got != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want %d", got, http.StatusUnprocessableEntity)
			}
		})
	}
}

func TestAdminDeleteSubmissionRejectsBadID(t *testing.T) {
	c := newFormContext(url.Values{})
	c.SetParamNames("id")
	c.SetParamValues("not-a-number")

	if err := AdminDeleteSubmission(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.Response().Status; got != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", got, http.StatusUnprocessableEntity)
	}
}
