package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

// InventoryCard wraps an InventoryItem with its derived display fields.
// Shared by the public /instock page and the /admin inventory views.
type InventoryCard struct {
	database.InventoryItem
	Code         string
	Label        string
	PriceDisplay string // empty means "call for price"
}

func toCard(it database.InventoryItem) InventoryCard {
	card := InventoryCard{
		InventoryItem: it,
		Code:          database.GenerateCode(it),
		Label:         database.Describe(it),
	}
	if it.PriceCents > 0 {
		card.PriceDisplay = fmt.Sprintf("$%.2f", float64(it.PriceCents)/100)
	}
	return card
}

func toCards(items []database.InventoryItem) []InventoryCard {
	cards := make([]InventoryCard, 0, len(items))
	for _, it := range items {
		cards = append(cards, toCard(it))
	}
	return cards
}

// LotGroup buckets cards under their lot number, in lot order, for the
// public page's per-status "Lot 1 / Lot 2 / Lot 3" subheadings.
type LotGroup struct {
	Lot   int
	Cards []InventoryCard
}

// groupByLot assumes cards are already ordered by lot (ListInventoryItems
// sorts ORDER BY lot ASC) and buckets consecutive same-lot cards together.
func groupByLot(cards []InventoryCard) []LotGroup {
	var groups []LotGroup
	for _, card := range cards {
		if n := len(groups); n > 0 && groups[n-1].Lot == card.Lot {
			groups[n-1].Cards = append(groups[n-1].Cards, card)
			continue
		}
		groups = append(groups, LotGroup{Lot: card.Lot, Cards: []InventoryCard{card}})
	}
	return groups
}

func filterByStatus(cards []InventoryCard, status string) []InventoryCard {
	filtered := make([]InventoryCard, 0, len(cards))
	for _, card := range cards {
		if card.Status == status {
			filtered = append(filtered, card)
		}
	}
	return filtered
}

func Instock(c echo.Context) error {
	items, err := database.ListInventoryItems()
	if err != nil {
		c.Logger().Error("list inventory failed:", err)
	}
	cards := toCards(items)

	return c.Render(http.StatusOK, "instock.html", map[string]interface{}{
		"Title":   "In Stock Sheds — Prebuilt Sheds LLC",
		"Empty":   len(cards) == 0,
		"All":     groupByLot(cards),
		"InStock": groupByLot(filterByStatus(cards, database.StatusInStock)),
		"OnHold":  groupByLot(filterByStatus(cards, database.StatusOnHold)),
		"Sold":    groupByLot(filterByStatus(cards, database.StatusSold)),
	})
}

// InstockInterest handles a customer expressing interest in one specific
// in-stock shed. The item is always looked up server-side from the trusted
// item_id — never trust a client-supplied description of which shed this is.
func InstockInterest(c echo.Context) error {
	// See the identical guard in Contact — same reasoning, same silent
	// success response.
	if isBotSubmission(c) {
		logSpamRejection(c, spamReason(c))
		return c.Render(http.StatusOK, "interest_success.html", nil)
	}

	itemID, err := strconv.ParseInt(c.FormValue("item_id"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Please try again from the In Stock page."))
	}

	name := strings.TrimSpace(c.FormValue("name"))
	phone := strings.TrimSpace(c.FormValue("phone"))
	email := strings.TrimSpace(c.FormValue("email"))
	message := strings.TrimSpace(c.FormValue("message"))

	if name == "" || phone == "" || email == "" {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Please fill in all required fields."))
	}
	if tooLong(shortFieldMax, name, phone, email) || tooLong(longFieldMax, message) {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("One of the fields is too long."))
	}

	item, err := database.GetInventoryItem(itemID)
	if err != nil {
		c.Logger().Error("get inventory item failed:", err)
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("That shed is no longer available. Please try another from the In Stock page."))
	}

	details := fmt.Sprintf("Interested in in-stock shed: %s\n%s", database.GenerateCode(item), database.Describe(item))
	if message != "" {
		details += "\n\nCustomer message:\n" + message
	}

	sub := database.ContactSubmission{
		Name:    name,
		Phone:   phone,
		Email:   email,
		Style:   item.Style,
		Size:    fmt.Sprintf("%dx%d", item.Width, item.Length),
		Details: details,
	}

	id, err := database.SaveContactSubmission(sub)
	if err != nil {
		c.Logger().Error("db save failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again or call us directly."))
	}

	queueEmail(id, sub)

	return c.Render(http.StatusOK, "interest_success.html", nil)
}
