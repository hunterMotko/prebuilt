package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func newFormContext(values url.Values) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func validInventoryFormValues() url.Values {
	return url.Values{
		"lot":         {"1"},
		"style":       {"Gable"},
		"width":       {"12"},
		"length":      {"24"},
		"siding_code": {"10"},
		"roof_code":   {"10"},
	}
}

// TestParseInventoryFormPriceRounding is a regression test for the B1 bug:
// int64(dollars*100) truncated instead of rounded, so float imprecision
// (e.g. 19.99*100 evaluating to 1998.9999999999998) could silently store a
// price one cent low.
func TestParseInventoryFormPriceRounding(t *testing.T) {
	cases := []struct {
		price     string
		wantCents int64
	}{
		{"19.99", 1999},
		{"4999.00", 499900},
		{"0.01", 1},
		{"100", 10000},
		{"", 0}, // no price set is valid — "call for price"
	}

	for _, tc := range cases {
		t.Run(tc.price, func(t *testing.T) {
			values := validInventoryFormValues()
			if tc.price != "" {
				values.Set("price", tc.price)
			}
			item, err := parseInventoryForm(newFormContext(values))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if item.PriceCents != tc.wantCents {
				t.Errorf("price %q -> PriceCents = %d, want %d", tc.price, item.PriceCents, tc.wantCents)
			}
		})
	}
}

// TestParseInventoryFormValidation is a regression test for B2: zero/negative
// width, length, or price used to pass validation entirely (the HTML min="1"
// attribute is client-side only).
func TestParseInventoryFormValidation(t *testing.T) {
	cases := []struct {
		name   string
		modify func(url.Values)
	}{
		{"negative width", func(v url.Values) { v.Set("width", "-5") }},
		{"zero width", func(v url.Values) { v.Set("width", "0") }},
		{"negative length", func(v url.Values) { v.Set("length", "-1") }},
		{"zero length", func(v url.Values) { v.Set("length", "0") }},
		{"negative price", func(v url.Values) { v.Set("price", "-10") }},
		{"missing style", func(v url.Values) { v.Set("style", "") }},
		{"non-numeric width", func(v url.Values) { v.Set("width", "abc") }},
		// ParseFloat accepts these, but int64(NaN)/int64(Inf) is garbage —
		// they must be rejected explicitly, along with absurd magnitudes.
		{"NaN price", func(v url.Values) { v.Set("price", "NaN") }},
		{"Inf price", func(v url.Values) { v.Set("price", "Inf") }},
		{"huge price", func(v url.Values) { v.Set("price", "1e300") }},
		// The forms only offer lot 1-3 and the three real styles, but a
		// crafted request can send anything.
		{"lot zero", func(v url.Values) { v.Set("lot", "0") }},
		{"lot out of range", func(v url.Values) { v.Set("lot", "4") }},
		{"unknown style", func(v url.Values) { v.Set("style", "Yurt") }},
		// Codes are free text since the reference tables were removed; the
		// maxlength attribute is client-side only.
		{"overlong siding code", func(v url.Values) { v.Set("siding_code", strings.Repeat("9", 21)) }},
		{"overlong roof code", func(v url.Values) { v.Set("roof_code", strings.Repeat("9", 21)) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := validInventoryFormValues()
			tc.modify(values)
			if _, err := parseInventoryForm(newFormContext(values)); err == nil {
				t.Errorf("expected an error, got none")
			}
		})
	}
}

func TestParseInventoryFormValid(t *testing.T) {
	item, err := parseInventoryForm(newFormContext(validInventoryFormValues()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.Lot != 1 || item.Style != "Gable" || item.Width != 12 || item.Length != 24 {
		t.Errorf("unexpected parsed item: %+v", item)
	}
}
