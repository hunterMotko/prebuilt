package database

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	StatusInStock = "in_stock"
	StatusOnHold  = "on_hold"
	StatusSold    = "sold"

	StyleBarn     = "Barn"
	StyleGable    = "Gable"
	StyleSkillion = "Skillion"

	PhotoCategoryExterior = "exterior"
	PhotoCategoryInterior = "interior"
	PhotoCategoryFeature  = "feature"
)

var styleLetters = map[string]string{
	StyleBarn:     "B",
	StyleGable:    "G",
	StyleSkillion: "S",
}

func createInventoryTables() {
	schema := `
	CREATE TABLE IF NOT EXISTS inventory_items (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		lot            INTEGER NOT NULL CHECK (lot IN (1, 2, 3)),
		style          TEXT NOT NULL CHECK (style IN ('Barn', 'Gable', 'Skillion')),
		width          INTEGER NOT NULL CHECK (width > 0),
		length         INTEGER NOT NULL CHECK (length > 0),
		siding_code    TEXT NOT NULL,
		roof_code      TEXT NOT NULL,
		status         TEXT NOT NULL DEFAULT 'in_stock' CHECK (status IN ('in_stock', 'on_hold', 'sold')),
		price_cents    INTEGER NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
		notes          TEXT NOT NULL DEFAULT '',
		created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS inventory_images (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		inventory_item_id INTEGER NOT NULL REFERENCES inventory_items(id) ON DELETE CASCADE,
		filename          TEXT NOT NULL,
		category          TEXT NOT NULL DEFAULT 'exterior' CHECK (category IN ('exterior', 'interior', 'feature')),
		created_at        DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_inventory_images_item ON inventory_images(inventory_item_id);`

	if _, err := DB.Exec(schema); err != nil {
		log.Fatal("failed to create inventory tables:", err)
	}

	migrateLegacyImageFilename()
	migrateInventoryItemsPositiveChecks()
	migrateDropColorTables()
}

// migrateDropColorTables removes the siding_colors/roof_colors reference
// tables. Codes were made free-text: the tables only ever fed swatches and
// display names, no foreign key referenced them, and the codes themselves
// live on inventory_items — so dropping them loses nothing. Idempotent.
func migrateDropColorTables() {
	if _, err := DB.Exec(
		`DROP TABLE IF EXISTS siding_colors; DROP TABLE IF EXISTS roof_colors;`,
	); err != nil {
		log.Fatal("failed to drop color reference tables:", err)
	}
}

// migrateLegacyImageFilename handles databases created before the
// inventory_images table existed, where each item had a single
// image_filename column instead. It's idempotent: it checks whether the
// column is still present before doing anything, so it's a no-op on
// databases that never had it (fresh installs) or have already been
// migrated.
func migrateLegacyImageFilename() {
	hasColumn, err := columnExists("inventory_items", "image_filename")
	if err != nil {
		log.Fatal("failed to inspect inventory_items schema:", err)
	}
	if !hasColumn {
		return
	}

	if _, err := DB.Exec(`
		INSERT INTO inventory_images (inventory_item_id, filename, category)
		SELECT id, image_filename, 'exterior' FROM inventory_items
		WHERE image_filename IS NOT NULL AND image_filename != ''`); err != nil {
		log.Fatal("failed to migrate legacy image_filename values:", err)
	}

	if _, err := DB.Exec(`ALTER TABLE inventory_items DROP COLUMN image_filename`); err != nil {
		log.Fatal("failed to drop legacy image_filename column:", err)
	}
}

// migrateInventoryItemsPositiveChecks adds CHECK constraints ensuring width,
// length, and price_cents can't be zero/negative. SQLite has no ALTER TABLE
// ADD CONSTRAINT, so this rebuilds the table following SQLite's own
// documented procedure for schema changes like this: disable foreign key
// enforcement, rebuild inside a transaction, verify referential integrity
// once done, then re-enable. Idempotent — checks the table's own recorded
// SQL for the constraint before doing anything, so it's a no-op on fresh
// installs (which already get the constraint from createInventoryTables'
// CREATE TABLE) or already-migrated databases.
func migrateInventoryItemsPositiveChecks() {
	var tableSQL string
	if err := DB.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='inventory_items'`,
	).Scan(&tableSQL); err != nil {
		log.Fatal("failed to inspect inventory_items schema:", err)
	}
	if strings.Contains(tableSQL, "CHECK (width > 0)") {
		return
	}

	if _, err := DB.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		log.Fatal("failed to disable foreign keys for migration:", err)
	}

	if err := rebuildInventoryItemsWithPositiveChecks(); err != nil {
		log.Fatal("failed to migrate inventory_items positive-value constraints:", err)
	}

	if _, err := DB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		log.Fatal("failed to re-enable foreign keys after migration:", err)
	}

	rows, err := DB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		log.Fatal("failed to run foreign_key_check after migration:", err)
	}
	hasIssues := rows.Next()
	rows.Close()
	if hasIssues {
		log.Fatal("foreign key check failed after inventory_items migration — data inconsistency detected")
	}
}

func rebuildInventoryItemsWithPositiveChecks() error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed

	if _, err := tx.Exec(`
		CREATE TABLE inventory_items_new (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			lot            INTEGER NOT NULL CHECK (lot IN (1, 2, 3)),
			style          TEXT NOT NULL CHECK (style IN ('Barn', 'Gable', 'Skillion')),
			width          INTEGER NOT NULL CHECK (width > 0),
			length         INTEGER NOT NULL CHECK (length > 0),
			siding_code    TEXT NOT NULL,
			roof_code      TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'in_stock' CHECK (status IN ('in_stock', 'on_hold', 'sold')),
			price_cents    INTEGER NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
			notes          TEXT NOT NULL DEFAULT '',
			created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		INSERT INTO inventory_items_new
		SELECT id, lot, style, width, length, siding_code, roof_code, status, price_cents, notes, created_at, updated_at
		FROM inventory_items`); err != nil {
		return err
	}

	if _, err := tx.Exec(`DROP TABLE inventory_items`); err != nil {
		return err
	}

	if _, err := tx.Exec(`ALTER TABLE inventory_items_new RENAME TO inventory_items`); err != nil {
		return err
	}

	return tx.Commit()
}

func columnExists(table, column string) (bool, error) {
	// table is always one of this file's own hardcoded constants, never
	// user input, so building the PRAGMA string directly is safe — SQLite
	// doesn't support binding table names as query parameters anyway.
	rows, err := DB.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// InventoryItem is one already-built shed sitting on a lot.
//
// Images is not a column: it is filled by a follow-up query in List/Get, so a
// zero-value item built by hand has it empty.
type InventoryItem struct {
	ID         int64
	Lot        int
	Style      string
	Width      int
	Length     int
	SidingCode string
	RoofCode   string
	Status     string
	PriceCents int64
	Notes      string
	CreatedAt  time.Time
	UpdatedAt  time.Time

	// Populated separately by List/Get below (a second query, not a JOIN —
	// joining would multiply the item's row per photo).
	Images []InventoryImage
}

// InventoryImage is one uploaded photo. Category is exterior, interior, or
// feature, and is admin-facing only — customers never see it.
//
// Filename is the server-generated name on disk, not anything the client sent.
// Rows cascade on delete from inventory_items; the files themselves do not, so
// whoever deletes an item must also remove its photo directory.
type InventoryImage struct {
	ID              int64
	InventoryItemID int64
	Filename        string
	Category        string
	CreatedAt       time.Time
}

const inventorySelect = `
	SELECT i.id, i.lot, i.style, i.width, i.length, i.siding_code, i.roof_code, i.status,
	       i.price_cents, i.notes, i.created_at, i.updated_at
	FROM inventory_items i`

func scanInventoryItem(row interface{ Scan(...any) error }) (InventoryItem, error) {
	var it InventoryItem
	err := row.Scan(
		&it.ID, &it.Lot, &it.Style, &it.Width, &it.Length, &it.SidingCode, &it.RoofCode, &it.Status,
		&it.PriceCents, &it.Notes, &it.CreatedAt, &it.UpdatedAt,
	)
	return it, err
}

// CreateInventoryItem inserts an item and returns its new id.
func CreateInventoryItem(item InventoryItem) (int64, error) {
	res, err := DB.Exec(
		`INSERT INTO inventory_items (lot, style, width, length, siding_code, roof_code, status, price_cents, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Lot, item.Style, item.Width, item.Length, item.SidingCode, item.RoofCode,
		item.Status, item.PriceCents, item.Notes,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListInventoryItems returns every item ordered by lot, then newest first,
// with photos populated.
//
// Photos come from one batched follow-up query rather than a join, because
// joining would multiply each item's row by its photo count and require
// de-duplicating on the way out.
func ListInventoryItems() ([]InventoryItem, error) {
	rows, err := DB.Query(inventorySelect + ` ORDER BY i.lot ASC, i.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []InventoryItem
	var ids []int64
	for rows.Next() {
		it, err := scanInventoryItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
		ids = append(ids, it.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	imagesByItem, err := ListInventoryImagesForItems(ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Images = imagesByItem[items[i].ID]
	}
	return items, nil
}

// GetInventoryItem returns one item with its colors and photos populated. It
// returns sql.ErrNoRows if no item has that id.
func GetInventoryItem(id int64) (InventoryItem, error) {
	row := DB.QueryRow(inventorySelect+` WHERE i.id = ?`, id)
	it, err := scanInventoryItem(row)
	if err != nil {
		return it, err
	}
	images, err := ListInventoryImages(id)
	if err != nil {
		return it, err
	}
	it.Images = images
	return it, nil
}

// UpdateInventoryItemStatus returns sql.ErrNoRows if no item has that id, so
// callers can distinguish "item doesn't exist" from a real database failure.
func UpdateInventoryItemStatus(id int64, status string) error {
	res, err := DB.Exec(
		`UPDATE inventory_items SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteInventoryItem removes the item row. inventory_images rows for it are
// removed automatically via ON DELETE CASCADE — but the actual photo files
// on disk are not; callers must also remove the item's photo directory.
func DeleteInventoryItem(id int64) error {
	_, err := DB.Exec(`DELETE FROM inventory_items WHERE id = ?`, id)
	return err
}

// UpdateInventoryItem overwrites the editable fields of an existing item by
// ID. Status is intentionally not touched here — that's managed exclusively
// by UpdateInventoryItemStatus via the list view's inline selector, so there
// isn't a second control that can drift out of sync with it.
func UpdateInventoryItem(item InventoryItem) error {
	_, err := DB.Exec(
		`UPDATE inventory_items
		 SET lot = ?, style = ?, width = ?, length = ?, siding_code = ?, roof_code = ?,
		     price_cents = ?, notes = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		item.Lot, item.Style, item.Width, item.Length, item.SidingCode, item.RoofCode,
		item.PriceCents, item.Notes, item.ID,
	)
	return err
}

// AddInventoryImage records one already-saved-to-disk photo against an item.
func AddInventoryImage(itemID int64, filename, category string) (int64, error) {
	res, err := DB.Exec(
		`INSERT INTO inventory_images (inventory_item_id, filename, category) VALUES (?, ?, ?)`,
		itemID, filename, category,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListInventoryImages returns one item's photos in upload order. Use
// ListInventoryImagesForItems when loading photos for more than one item.
func ListInventoryImages(itemID int64) ([]InventoryImage, error) {
	rows, err := DB.Query(
		`SELECT id, inventory_item_id, filename, category, created_at
		 FROM inventory_images WHERE inventory_item_id = ? ORDER BY id ASC`,
		itemID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInventoryImages(rows)
}

// ListInventoryImagesForItems batches the image lookup for many items into a
// single query (grouped in Go afterward) to avoid an N+1 query per item.
func ListInventoryImagesForItems(itemIDs []int64) (map[int64][]InventoryImage, error) {
	result := map[int64][]InventoryImage{}
	if len(itemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(itemIDs))
	args := make([]any, len(itemIDs))
	for i, id := range itemIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT id, inventory_item_id, filename, category, created_at
		 FROM inventory_images WHERE inventory_item_id IN (%s) ORDER BY inventory_item_id, id ASC`,
		strings.Join(placeholders, ","),
	)

	rows, err := DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	images, err := scanInventoryImages(rows)
	if err != nil {
		return nil, err
	}
	for _, img := range images {
		result[img.InventoryItemID] = append(result[img.InventoryItemID], img)
	}
	return result, nil
}

func scanInventoryImages(rows *sql.Rows) ([]InventoryImage, error) {
	var images []InventoryImage
	for rows.Next() {
		var img InventoryImage
		if err := rows.Scan(&img.ID, &img.InventoryItemID, &img.Filename, &img.Category, &img.CreatedAt); err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, rows.Err()
}

// GetInventoryImage returns one photo row, or sql.ErrNoRows if no photo has that
// id. Callers need it to learn the filename and owning item before deleting the
// file from disk.
func GetInventoryImage(id int64) (InventoryImage, error) {
	var img InventoryImage
	err := DB.QueryRow(
		`SELECT id, inventory_item_id, filename, category, created_at FROM inventory_images WHERE id = ?`,
		id,
	).Scan(&img.ID, &img.InventoryItemID, &img.Filename, &img.Category, &img.CreatedAt)
	return img, err
}

// DeleteInventoryImage removes the DB row only; the caller is responsible
// for unlinking the actual file (this package doesn't know the photo
// directory convention — that's handlers/uploads.go's concern).
func DeleteInventoryImage(id int64) error {
	_, err := DB.Exec(`DELETE FROM inventory_images WHERE id = ?`, id)
	return err
}

// GenerateCode builds the human-readable lot tag, e.g. "1-G-1224-2345".
// NOT unique — the DB id is the true key.
func GenerateCode(item InventoryItem) string {
	return fmt.Sprintf("%d-%s-%02d%02d-%s%s",
		item.Lot, styleLetters[item.Style], item.Width, item.Length, item.SidingCode, item.RoofCode)
}

// Describe builds a full human-readable breakdown, reusable anywhere the raw
// code isn't enough (item detail, a sale record, a lot-move log). Pure
// formatting on the item's own fields — safe on a bare struct.
func Describe(item InventoryItem) string {
	return fmt.Sprintf("Lot %d · %d×%d %s · Siding %s · Roof %s",
		item.Lot, item.Width, item.Length, item.Style,
		item.SidingCode, item.RoofCode)
}
