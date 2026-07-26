package database

import "testing"

func TestGenerateCode(t *testing.T) {
	cases := []struct {
		name string
		item InventoryItem
		want string
	}{
		{
			name: "double-digit dimensions",
			item: InventoryItem{Lot: 1, Style: StyleGable, Width: 12, Length: 24, SidingCode: "23", RoofCode: "45"},
			want: "1-G-1224-2345",
		},
		{
			name: "single-digit dimensions get zero-padded",
			item: InventoryItem{Lot: 2, Style: StyleBarn, Width: 8, Length: 10, SidingCode: "10", RoofCode: "20"},
			want: "2-B-0810-1020",
		},
		{
			name: "skillion style letter",
			item: InventoryItem{Lot: 3, Style: StyleSkillion, Width: 14, Length: 28, SidingCode: "01", RoofCode: "02"},
			want: "3-S-1428-0102",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GenerateCode(tc.item); got != tc.want {
				t.Errorf("GenerateCode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	item := InventoryItem{
		Lot: 1, Width: 12, Length: 24, Style: StyleGable,
		SidingName: "Barn Red", SidingHex: "#8B2E2E",
		RoofName: "Charcoal", RoofHex: "#3A3A3A",
	}

	want := "Lot 1 · 12×24 Gable · Siding: Barn Red (#8B2E2E) · Roof: Charcoal (#3A3A3A)"
	if got := Describe(item); got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}
