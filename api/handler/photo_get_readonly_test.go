package handler

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	modernsqlite "modernc.org/sqlite"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/imagethumb"
	"knox-media/internal/photoclass"
	"knox-media/internal/photoface"
	"knox-media/internal/photogeocode"
	"knox-media/internal/store"
)

const writeCountingSQLiteDriverName = "sqlite-photo-get-write-counting"

var registerWriteCountingSQLite sync.Once
var activeWriteCountingSQLiteDriver *writeCountingDriver

type writeCountingDriver struct {
	base     driver.Driver
	counters sync.Map
}

func (d *writeCountingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	value, ok := d.counters.Load(name)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("write counting SQLite DSN is not registered")
	}
	return &writeCountingConn{Conn: conn, writes: value.(*atomic.Int64)}, nil
}

type writeCountingConn struct {
	driver.Conn
	writes *atomic.Int64
}

func (c *writeCountingConn) Begin() (driver.Tx, error) {
	c.writes.Add(1)
	return c.Conn.Begin()
}

func (c *writeCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.writes.Add(1)
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return b.BeginTx(ctx, opts)
}

func (c *writeCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.writes.Add(1)
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, query, args)
}

func (c *writeCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func openPhotoGETWriteCountingDB(t *testing.T) (*sql.DB, *atomic.Int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "photo-get.sqlite")
	bootstrap, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bootstrap.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES(1,'photos','photo','E:/photos',1);
		INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(2,'admin','x','admin',1,'all');
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(10,1,'photo-10','Photo 10','E:/photos/10.jpg','image','{"photo":{"tags":["保存"," custom ","custom"],"ai_tags":["保存"]}}','active');
		INSERT INTO photo_person(id,library_id,label,media_count) VALUES(20,1,'Person',1);
		INSERT INTO photo_classify_task(media_id,status) VALUES(10,'pending');
		INSERT INTO photo_location_task(media_id,library_id,status) VALUES(10,1,'pending');
		INSERT INTO photo_face_task(media_id,library_id,status) VALUES(10,1,'pending');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Close(); err != nil {
		t.Fatal(err)
	}

	registerWriteCountingSQLite.Do(func() {
		activeWriteCountingSQLiteDriver = &writeCountingDriver{base: &modernsqlite.Driver{}}
		sql.Register(writeCountingSQLiteDriverName, activeWriteCountingSQLiteDriver)
	})
	counter := &atomic.Int64{}
	activeWriteCountingSQLiteDriver.counters.Store(path, counter)
	t.Cleanup(func() { activeWriteCountingSQLiteDriver.counters.Delete(path) })
	db, err := sql.Open(writeCountingSQLiteDriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db, counter
}

func TestPhotoGETHandlersDoNotWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, writes := openPhotoGETWriteCountingDB(t)
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	h.PhotoClassifyWorker = photoclass.NewWorker(db, nil, "", "", "", nil)
	h.PhotoLocationWorker = photogeocode.NewWorker(db, nil, nil)
	h.PhotoFaceWorker = photoface.NewWorker(db, nil, nil, "", "", "", nil)

	tests := []struct {
		name, target string
		call         func(*gin.Context)
	}{
		{"list image rows", "/api/v1/media?library_id=1&file_type=image", h.ListMedia},
		{"categories", "/api/v1/library/1/photo/categories", h.ListPhotoCategories},
		{"places", "/api/v1/library/1/photo/places", h.ListPhotoPlaces},
		{"persons", "/api/v1/library/1/photo/persons", h.ListPhotoPersons},
		{"classify progress", "/api/v1/library/1/photo/classify/progress", h.PhotoClassifyProgress},
		{"location progress", "/api/v1/library/1/photo/locations/progress", h.PhotoLocationProgress},
		{"face progress", "/api/v1/library/1/photo/faces/progress", h.PhotoFaceProgress},
		{"photo detail", "/api/v1/media/10/photo", h.PhotoPreviewInfo},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writes.Store(0)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)
			setUserCtx(c, 2, "admin", "admin")
			c.Params = gin.Params{{Key: "id", Value: map[bool]string{true: "10", false: "1"}[tc.name == "photo detail"]}}
			tc.call(c)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if got := writes.Load(); got != 0 {
				t.Fatalf("GET %s performed %d Exec/Begin writes", tc.target, got)
			}
		})
	}
}

func TestPhotoVariantGETsAreServeOnlyOnHitAndMiss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, writes := openPhotoGETWriteCountingDB(t)
	preview := t.TempDir()
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: preview}}}, runningScans: map[int64]scanRuntime{}}
	paths := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), 10)
	if err := os.MkdirAll(filepath.Dir(paths.Thumb), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Thumb, []byte("existing-jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := snapshotFiles(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, variant string
		want          int
	}{
		{"thumb hit", "thumb", http.StatusOK}, {"medium miss", "medium", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes.Store(0)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10/photo/"+tc.variant+".jpg", nil)
			c.Params = gin.Params{{Key: "id", Value: "10"}}
			setUserCtx(c, 2, "admin", "admin")
			h.servePhotoVariant(c, tc.variant)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if got := writes.Load(); got != 0 {
				t.Fatalf("writes=%d", got)
			}
		})
	}
	after, err := snapshotFiles(preview)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("GET changed files before=%v after=%v", before, after)
	}
}

func snapshotFiles(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, fmt.Sprintf("%s:%d:%d", rel, info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	return out, err
}

func TestPhotoAuxiliaryGETsRequireLibraryAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := openPhotoGETWriteCountingDB(t)
	if _, err := db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(3,'selected','x','user',1,'selected')`); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	h.PhotoClassifyWorker = photoclass.NewWorker(db, nil, "", "", "", nil)
	h.PhotoLocationWorker = photogeocode.NewWorker(db, nil, nil)
	h.PhotoFaceWorker = photoface.NewWorker(db, nil, nil, "", "", "", nil)
	for _, tc := range []struct {
		name string
		call func(*gin.Context)
	}{
		{"categories", h.ListPhotoCategories}, {"places", h.ListPhotoPlaces}, {"persons", h.ListPhotoPersons},
		{"classify progress", h.PhotoClassifyProgress}, {"location progress", h.PhotoLocationProgress}, {"face progress", h.PhotoFaceProgress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library/1/photo", nil)
			c.Params = gin.Params{{Key: "id", Value: "1"}}
			setUserCtx(c, 3, "selected", "user")
			tc.call(c)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestFolderScopedUserCannotReadPhotoAggregates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := openPhotoGETWriteCountingDB(t)
	if _, err := db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(4,'folder-user','x','user',1,'selected');
		INSERT INTO user_library_permission(user_id,library_id) VALUES(4,1);
		INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(4,1,'E:/photos/allowed')`); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	h.PhotoClassifyWorker = photoclass.NewWorker(db, nil, "", "", "", nil)
	h.PhotoLocationWorker = photogeocode.NewWorker(db, nil, nil)
	h.PhotoFaceWorker = photoface.NewWorker(db, nil, nil, "", "", "", nil)
	for _, tc := range []struct {
		name string
		call func(*gin.Context)
	}{
		{"categories", h.ListPhotoCategories}, {"places", h.ListPhotoPlaces}, {"persons", h.ListPhotoPersons},
		{"classify progress", h.PhotoClassifyProgress}, {"location progress", h.PhotoLocationProgress}, {"face progress", h.PhotoFaceProgress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library/1/photo", nil)
			c.Params = gin.Params{{Key: "id", Value: "1"}}
			setUserCtx(c, 4, "user", "folder-user")
			tc.call(c)
			if w.Code != http.StatusForbidden || w.Body.String() != `{"error":"folder-scoped photo aggregate unavailable"}` {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestFolderScopedUserCanReadAllowedPhotoAssetsButNotHidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := openPhotoGETWriteCountingDB(t)
	preview := t.TempDir()
	_, err := db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(4,'folder-user','x','user',1,'selected');
		INSERT INTO user_library_permission(user_id,library_id) VALUES(4,1);
		INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(4,1,'E:/photos/allowed');
		UPDATE media SET file_path='E:/photos/allowed/10.jpg' WHERE id=10;
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(11,1,'photo-11','Hidden','E:/photos/hidden/11.jpg','image','active');
		INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(31,10,1,.1,.1,.5,.5),(32,11,1,.1,.1,.5,.5)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: preview}}}, runningScans: map[int64]scanRuntime{}}
	allowedPaths := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), 10)
	if err := os.MkdirAll(filepath.Dir(allowedPaths.Thumb), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowedPaths.Thumb, []byte("jpeg"), 0644); err != nil {
		t.Fatal(err)
	}
	facePath := photoface.ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 31)
	if err := os.MkdirAll(filepath.Dir(facePath), 0755); err != nil {
		t.Fatal(err)
	}
	var faceJPEG bytes.Buffer
	if err := jpeg.Encode(&faceJPEG, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(facePath, faceJPEG.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, id string
		call     func(*gin.Context)
		want     int
	}{
		{"allowed detail", "10", h.PhotoPreviewInfo, http.StatusOK}, {"hidden detail", "11", h.PhotoPreviewInfo, http.StatusForbidden},
		{"allowed thumb", "10", h.ServePhotoThumb, http.StatusOK}, {"hidden thumb", "11", h.ServePhotoThumb, http.StatusForbidden},
		{"allowed face", "31", h.ServePhotoFaceThumb, http.StatusOK}, {"hidden face", "32", h.ServePhotoFaceThumb, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/photo", nil)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			setUserCtx(c, 4, "user", "folder-user")
			tc.call(c)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want == http.StatusForbidden && w.Body.String() != `{"error":"folder access denied"}` {
				t.Fatalf("unexpected denial: %s", w.Body.String())
			}
		})
	}
}
