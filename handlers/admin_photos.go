package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/hunterMotko/prebuilt/database"
)

// AdminDeleteImage removes one photo from an item, both the row and the file,
// and returns an empty body for htmx to swap the thumbnail away with.
func AdminDeleteImage(c echo.Context) error {
	imageID, err := strconv.ParseInt(c.Param("imageId"), 10, 64)
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, errorHTML("Invalid photo id."))
	}

	img, err := database.GetInventoryImage(imageID)
	if err != nil {
		return c.HTML(http.StatusNotFound, errorHTML("Photo not found."))
	}

	if err := database.DeleteInventoryImage(imageID); err != nil {
		c.Logger().Error("delete inventory image failed:", err)
		return c.HTML(http.StatusInternalServerError, errorHTML("Something went wrong. Please try again."))
	}

	if err := os.Remove(filepath.Join(inventoryImageDir(img.InventoryItemID), img.Filename)); err != nil {
		c.Logger().Error("delete inventory image file failed:", err)
	}

	return c.NoContent(http.StatusOK)
}
