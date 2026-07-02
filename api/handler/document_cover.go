package handler

import (
	"knox-media/internal/doccover"
)

func extractEPUBCover(epubPath, cachePath string) string {
	return doccover.ExtractEPUBCover(epubPath, cachePath)
}
