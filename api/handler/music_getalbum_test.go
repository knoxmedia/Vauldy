package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func TestGetAlbumReturnsTracksFromRealDB(t *testing.T) {
	dbPath := filepath.Join("..", "..", "data", "knox-media.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("dev database not present")
	}
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := &Handler{App: &app.App{DB: db}}
	gin.SetMode(gin.TestMode)

	for _, albumID := range []int64{1, 2, 5} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(albumID, 10)}}

		h.GetAlbum(c)
		if w.Code != http.StatusOK {
			t.Fatalf("album %d status=%d body=%s", albumID, w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		tracks, _ := resp["tracks"].([]any)
		tc, _ := resp["track_count"].(float64)
		if len(tracks) == 0 || int(tc) == 0 {
			t.Fatalf("album %d expected tracks, got track_count=%v tracks=%v body=%s", albumID, tc, len(tracks), w.Body.String())
		}
	}
}
