package handler

import (
	"context"
	"knox-media/internal/mediastore"
	"log"
	"path/filepath"
	"strings"
	"time"
)

func (h *Handler) mediaCleanupRoots() []string {
	if h == nil || h.App == nil || h.App.Config == nil {
		return nil
	}
	d := h.App.Config.Data
	seen := map[string]bool{}
	var roots []string
	for _, p := range []string{d.Dir, d.Preview, d.Transcode, d.Subtitle, d.Upload, d.Chunks, d.ATracks, d.Keyframes, d.MetadataLibrary, d.Encrypted} {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			roots = append(roots, p)
		}
	}
	return roots
}
func (h *Handler) StartMediaFileCleanupLoop(ctx context.Context) {
	if h == nil || h.App == nil || h.App.DB == nil {
		<-ctx.Done()
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_, failed, err := mediastore.RunCleanupBatch(ctx, h.App.DB, h.mediaCleanupRoots(), 64)
			if err != nil && ctx.Err() == nil {
				log.Printf("media file cleanup: %v", err)
			} else if failed > 0 {
				log.Printf("media file cleanup: failed=%d", failed)
			}
			timer.Reset(time.Minute)
		}
	}
}
