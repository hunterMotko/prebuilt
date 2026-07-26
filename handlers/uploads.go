package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

const (
	maxPhotoBytes    = 8 << 20 // 8MB per photo
	maxPhotosPerItem = 12
)

var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// photoFormFields maps each upload form field name to the category its
// files should be tagged with.
var photoFormFields = map[string]string{
	"exterior_photos": database.PhotoCategoryExterior,
	"interior_photos": database.PhotoCategoryInterior,
	"feature_photos":  database.PhotoCategoryFeature,
}

func inventoryImageDir(itemID int64) string {
	return filepath.Join("public", "images", "inventory", fmt.Sprintf("%d", itemID))
}

// validatePhotoUploads checks every uploaded file's size and real content
// type across all three category fields WITHOUT writing anything to disk or
// the database. Called before an item is created (or before edits are
// saved) so a bad upload never leaves an orphaned item or a half-applied
// edit behind — validation fully completes before any commit happens.
func validatePhotoUploads(c echo.Context) (map[string][]*multipart.FileHeader, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}

	byCategory := map[string][]*multipart.FileHeader{}
	for field, category := range photoFormFields {
		files := form.File[field]
		for _, fh := range files {
			if fh.Size > maxPhotoBytes {
				return nil, fmt.Errorf("%s is too large (max 8MB)", fh.Filename)
			}
			if _, err := sniffExt(fh); err != nil {
				return nil, err
			}
		}
		if len(files) > 0 {
			byCategory[category] = files
		}
	}

	return byCategory, nil
}

// checkPhotoCap enforces the total-per-item photo limit across whatever the
// item already has plus what's about to be added.
func checkPhotoCap(existingCount int, byCategory map[string][]*multipart.FileHeader) error {
	newCount := 0
	for _, files := range byCategory {
		newCount += len(files)
	}
	if existingCount+newCount > maxPhotosPerItem {
		return fmt.Errorf("too many photos — this shed would have %d, max is %d per shed",
			existingCount+newCount, maxPhotosPerItem)
	}
	return nil
}

// sniffExt reads a file's real content (not its claimed extension or
// Content-Type header) to determine its true type, rejecting anything that
// isn't a supported image format.
func sniffExt(fh *multipart.FileHeader) (string, error) {
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()

	sniff := make([]byte, 512)
	n, err := f.Read(sniff)
	if err != nil && err != io.EOF {
		return "", err
	}

	ext, ok := allowedImageTypes[http.DetectContentType(sniff[:n])]
	if !ok {
		return "", fmt.Errorf("%s isn't a supported image type (jpg/png/webp only)", fh.Filename)
	}
	return ext, nil
}

// savePhotoBatches writes every validated file to the item's photo
// directory under a random server-generated filename (never the client's
// original filename) and records each as an inventory_images row.
func savePhotoBatches(itemID int64, byCategory map[string][]*multipart.FileHeader) error {
	if len(byCategory) == 0 {
		return nil
	}

	dir := inventoryImageDir(itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for category, files := range byCategory {
		for _, fh := range files {
			if err := savePhoto(dir, itemID, fh, category); err != nil {
				return err
			}
		}
	}
	return nil
}

func savePhoto(dir string, itemID int64, fh *multipart.FileHeader, category string) error {
	ext, err := sniffExt(fh)
	if err != nil {
		return err
	}

	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	filename, err := randomFilename(ext)
	if err != nil {
		return err
	}

	dst, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}

	if _, err := database.AddInventoryImage(itemID, filename, category); err != nil {
		os.Remove(filepath.Join(dir, filename))
		return err
	}
	return nil
}

// randomFilename never trusts the client's original filename (path
// traversal, weird characters, collisions) — it generates its own.
func randomFilename(ext string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf) + ext, nil
}
