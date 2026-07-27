package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

// csrfToken reads the token the CSRF middleware generated for this request
// (see main.go's adminCSRF config) so it can be embedded in a page — as a
// hidden form field for plain POSTs, or a <meta> tag for htmx requests.
func csrfToken(c echo.Context) string {
	token, _ := c.Get("csrf").(string)
	return token
}

func AdminList(c echo.Context) error {
	items, err := database.ListInventoryItems()
	if err != nil {
		c.Logger().Error("list inventory failed:", err)
	}

	return c.Render(http.StatusOK, "admin_list.html", map[string]any{
		"Title":       "Inventory — Admin",
		"Cards":       toCards(items),
		"CSRFToken":   csrfToken(c),
		"Undelivered": undeliveredCount(c),
	})
}

func AdminNewItemForm(c echo.Context) error {
	siding, err := database.ListSidingColors()
	if err != nil {
		c.Logger().Error("list siding colors failed:", err)
	}
	roof, err := database.ListRoofColors()
	if err != nil {
		c.Logger().Error("list roof colors failed:", err)
	}

	return c.Render(http.StatusOK, "admin_new.html", map[string]any{
		"Title":        "New Item — Admin",
		"SidingColors": siding,
		"RoofColors":   roof,
		"CSRFToken":    csrfToken(c),
	})
}

// parseInventoryForm reads and validates the fields shared by the create and
// edit forms (everything except id/status, which each caller sets itself).
func parseInventoryForm(c echo.Context) (database.InventoryItem, error) {
	lot, lotErr := strconv.Atoi(c.FormValue("lot"))
	width, widthErr := strconv.Atoi(c.FormValue("width"))
	length, lengthErr := strconv.Atoi(c.FormValue("length"))
	style := c.FormValue("style")
	sidingCode := strings.TrimSpace(c.FormValue("siding_code"))
	roofCode := strings.TrimSpace(c.FormValue("roof_code"))

	if lotErr != nil || widthErr != nil || lengthErr != nil ||
		style == "" || sidingCode == "" || roofCode == "" {
		return database.InventoryItem{}, errors.New("Please fill in all required fields.")
	}
	if width <= 0 || length <= 0 {
		return database.InventoryItem{}, errors.New("Width and length must be greater than zero.")
	}
	// The forms only offer valid values for these, but validating here too
	// turns a crafted request into a clean 422 instead of letting it hit the
	// DB CHECK constraint and surface as a generic 500.
	if lot < 1 || lot > 3 {
		return database.InventoryItem{}, errors.New("Lot must be 1, 2, or 3.")
	}
	switch style {
	case database.StyleBarn, database.StyleGable, database.StyleSkillion:
	default:
		return database.InventoryItem{}, errors.New("Invalid style.")
	}

	var priceCents int64
	if priceStr := strings.TrimSpace(c.FormValue("price")); priceStr != "" {
		dollars, err := strconv.ParseFloat(priceStr, 64)
		if err != nil {
			return database.InventoryItem{}, errors.New("Price must be a number.")
		}
		// ParseFloat happily accepts "NaN" and "Inf", and converting either
		// (or anything huge) to int64 produces garbage — reject them along
		// with anything past a sanity ceiling no shed will ever cost.
		if math.IsNaN(dollars) || math.IsInf(dollars, 0) || dollars > 10_000_000 {
			return database.InventoryItem{}, errors.New("Price must be a number.")
		}
		if dollars < 0 {
			return database.InventoryItem{}, errors.New("Price can't be negative.")
		}
		// Round rather than truncate — float64 multiplication doesn't always
		// land on an exact integer (e.g. 19.99*100 can evaluate to
		// 1998.9999999999998), and truncating would silently store a penny low.
		priceCents = int64(math.Round(dollars * 100))
	}

	return database.InventoryItem{
		Lot:        lot,
		Style:      style,
		Width:      width,
		Length:     length,
		SidingCode: sidingCode,
		RoofCode:   roofCode,
		PriceCents: priceCents,
		Notes:      strings.TrimSpace(c.FormValue("notes")),
	}, nil
}

func AdminCreateItem(c echo.Context) error {
	item, err := parseInventoryForm(c)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML(err.Error()))
	}

	byCategory, err := validatePhotoUploads(c)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML(err.Error()))
	}
	if err := checkPhotoCap(0, byCategory); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML(err.Error()))
	}

	item.Status = database.StatusInStock
	id, err := database.CreateInventoryItem(item)
	if err != nil {
		c.Logger().Error("create inventory item failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	if err := savePhotoBatches(id, byCategory); err != nil {
		c.Logger().Error("save inventory photos failed:", err)
	}

	return c.Redirect(http.StatusSeeOther, "/admin")
}

func AdminEditItemForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid item id."))
	}

	item, err := database.GetInventoryItem(id)
	if err != nil {
		return c.HTML(http.StatusNotFound, errorHTML("Item not found."))
	}

	siding, err := database.ListSidingColors()
	if err != nil {
		c.Logger().Error("list siding colors failed:", err)
	}
	roof, err := database.ListRoofColors()
	if err != nil {
		c.Logger().Error("list roof colors failed:", err)
	}

	priceDollars := ""
	if item.PriceCents > 0 {
		priceDollars = fmt.Sprintf("%.2f", float64(item.PriceCents)/100)
	}

	return c.Render(http.StatusOK, "admin_edit.html", map[string]any{
		"Title":        "Edit Item — Admin",
		"Item":         item,
		"PriceDollars": priceDollars,
		"SidingColors": siding,
		"RoofColors":   roof,
		"CSRFToken":    csrfToken(c),
	})
}

func AdminUpdateItem(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid item id."))
	}

	item, err := parseInventoryForm(c)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML(err.Error()))
	}
	item.ID = id

	byCategory, err := validatePhotoUploads(c)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML(err.Error()))
	}

	existing, err := database.ListInventoryImages(id)
	if err != nil {
		c.Logger().Error("list inventory images failed:", err)
	}
	if err := checkPhotoCap(len(existing), byCategory); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML(err.Error()))
	}

	if err := database.UpdateInventoryItem(item); err != nil {
		c.Logger().Error("update inventory item failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	if err := savePhotoBatches(id, byCategory); err != nil {
		c.Logger().Error("save inventory photos failed:", err)
	}

	return c.Redirect(http.StatusSeeOther, "/admin")
}

func AdminUpdateStatus(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid item id."))
	}

	status := c.FormValue("status")
	switch status {
	case database.StatusInStock, database.StatusOnHold, database.StatusSold:
	default:
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid status."))
	}

	if err := database.UpdateInventoryItemStatus(id, status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.HTML(http.StatusNotFound, errorHTML("Item not found."))
		}
		c.Logger().Error("update inventory status failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	item, err := database.GetInventoryItem(id)
	if err != nil {
		c.Logger().Error("get inventory item failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	return c.Render(http.StatusOK, "admin_item_row.html", toCard(item))
}

func AdminDeleteItem(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid item id."))
	}

	// inventory_images rows are removed automatically via ON DELETE CASCADE,
	// but the actual files on disk aren't — remove the whole per-item photo
	// directory afterward to clean those up too.
	if err := database.DeleteInventoryItem(id); err != nil {
		c.Logger().Error("delete inventory item failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	if err := os.RemoveAll(inventoryImageDir(id)); err != nil {
		c.Logger().Error("delete inventory photo directory failed:", err)
	}

	return c.NoContent(http.StatusOK)
}
