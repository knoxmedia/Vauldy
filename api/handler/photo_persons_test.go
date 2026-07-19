package handler

import (
	"bytes"
	"context"
	"github.com/gin-gonic/gin"
	"knox-media/internal/app"
	"knox-media/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestUpdatePhotoPersonDeniesFolderScopedUserAndAllowsFullLibraryUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "person-auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO library_folder(id,library_id,path) VALUES(3,1,'x/folder'); INSERT INTO photo_person(id,library_id,label) VALUES(5,1,'Old'); INSERT INTO user(id,username,password,role,library_scope) VALUES(1,'folder','x','user','selected'),(2,'full','x','user','selected'); INSERT INTO user_library_permission(user_id,library_id) VALUES(1,1),(2,1); INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(1,1,'x/folder')`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	call := func(user int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/library/1/photo/persons/5", bytes.NewBufferString(`{"name":"New"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: "1"}, {Key: "personId", Value: "5"}}
		setUserCtx(c, user, "user", "user")
		h.UpdatePhotoPerson(c)
		return w
	}
	if w := call(1); w.Code != http.StatusForbidden {
		t.Fatalf("folder status=%d body=%s", w.Code, w.Body.String())
	}
	var name string
	if err = db.QueryRowContext(context.Background(), `SELECT label FROM photo_person WHERE id=5`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Old" {
		t.Fatalf("folder changed name=%q", name)
	}
	if w := call(2); w.Code != http.StatusOK {
		t.Fatalf("full status=%d body=%s", w.Code, w.Body.String())
	}
	if err = db.QueryRowContext(context.Background(), `SELECT label FROM photo_person WHERE id=5`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "New" {
		t.Fatalf("name=%q", name)
	}
}
