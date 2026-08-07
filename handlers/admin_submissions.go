package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

// submissionListLimit caps the page size. Contact volume for a business this
// size is low, so one page is enough; add paging if that stops being true.
const submissionListLimit = 200

// SubmissionCard decorates a submission with display-ready delivery state, so
// the template contains no status logic.
type SubmissionCard struct {
	database.ContactSubmission
	StatusLabel string
	StatusClass string
	Warning     bool
}

// describeDelivery translates a stored status into what it actually means for
// the business. Wording is deliberately careful: SMTP acceptance is not proof
// of inbox delivery, so this never claims the email was "received".
func describeDelivery(status string) (label, class string, warning bool) {
	switch status {
	case database.EmailSent:
		return "Emailed", "ok", false
	case database.EmailFailed:
		return "Email FAILED", "fail", true
	case database.EmailSkipped:
		return "No email sent", "warn", true
	default: // EmailPending
		return "Unconfirmed", "warn", true
	}
}

// AdminSubmissions renders recent contact-form leads with their delivery status,
// so a submission whose email failed is visible rather than silently lost. This
// is what makes the fire-and-forget send safe.
func AdminSubmissions(c echo.Context) error {
	subs, err := database.ListContactSubmissions(submissionListLimit)
	if err != nil {
		c.Logger().Error("list contact submissions failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Could not load submissions."))
	}

	cards := make([]SubmissionCard, 0, len(subs))
	undelivered := 0
	for _, s := range subs {
		card := toSubmissionCard(s)
		if card.Warning {
			undelivered++
		}
		cards = append(cards, card)
	}

	return c.Render(http.StatusOK, "admin_submissions.html", map[string]any{
		"Title":       "Contact Submissions — Admin",
		"Cards":       cards,
		"Undelivered": undelivered,
		"CSRFToken":   csrfToken(c),
	})
}

// toSubmissionCard wraps one submission for the row template.
func toSubmissionCard(s database.ContactSubmission) SubmissionCard {
	label, class, warning := describeDelivery(s.EmailStatus)
	return SubmissionCard{
		ContactSubmission: s,
		StatusLabel:       label,
		StatusClass:       class,
		Warning:           warning,
	}
}

// AdminUpdateSubmissionLeadStatus sets the admin triage flag on one lead and
// returns the re-rendered admin_submission_row.html fragment for htmx to swap
// in place. Any value outside the five Lead* states is rejected with 422.
func AdminUpdateSubmissionLeadStatus(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid submission id."))
	}

	status := c.FormValue("status")
	switch status {
	case database.LeadNew, database.LeadConfirmed, database.LeadNotSold,
		database.LeadBadEmail, database.LeadSuspicious:
	default:
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid status."))
	}

	if err := database.UpdateSubmissionLeadStatus(id, status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.HTML(http.StatusNotFound, errorHTML("Submission not found."))
		}
		c.Logger().Error("update submission lead status failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	sub, err := database.GetContactSubmission(id)
	if err != nil {
		c.Logger().Error("get contact submission failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	return c.Render(http.StatusOK, "admin_submission_row.html", toSubmissionCard(sub))
}

// AdminDeleteSubmission removes a lead permanently. The empty 200 body lets
// htmx's outerHTML swap delete the row in place.
func AdminDeleteSubmission(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid submission id."))
	}

	if err := database.DeleteContactSubmission(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.HTML(http.StatusNotFound, errorHTML("Submission not found."))
		}
		c.Logger().Error("delete contact submission failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	return c.NoContent(http.StatusOK)
}

// undeliveredCount feeds the warning banner on the inventory page. Errors are
// swallowed to zero on purpose: a broken count must not take down the main
// admin screen, and the submissions page itself is the authoritative view.
func undeliveredCount(c echo.Context) int {
	n, err := database.CountUndeliveredSubmissions()
	if err != nil {
		c.Logger().Error("count undelivered submissions failed:", err)
		return 0
	}
	return n
}
