package handler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	modernsqlite "modernc.org/sqlite"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

const countingSQLiteDriverName = "sqlite-list-media-counting"

var registerCountingSQLite sync.Once
var activeCountingSQLiteDriver *countingDriver

type countingDriver struct {
	base     driver.Driver
	counters sync.Map // map[string]*atomic.Int64, keyed by exact DSN
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	value, ok := d.counters.Load(name)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("counting sqlite DSN is not registered")
	}
	return &countingConn{Conn: conn, counter: value.(*atomic.Int64)}, nil
}

type countingConn struct {
	driver.Conn
	counter *atomic.Int64
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return b.BeginTx(ctx, opts)
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.Add(1)
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.Add(1)
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, query, args)
}

func openCountingSQLitePath(t *testing.T, path string) (*sql.DB, *atomic.Int64) {
	t.Helper()
	registerCountingSQLite.Do(func() {
		activeCountingSQLiteDriver = &countingDriver{base: &modernsqlite.Driver{}}
		sql.Register(countingSQLiteDriverName, activeCountingSQLiteDriver)
	})
	counter := &atomic.Int64{}
	activeCountingSQLiteDriver.counters.Store(path, counter)
	t.Cleanup(func() { activeCountingSQLiteDriver.counters.Delete(path) })
	db, err := sql.Open(countingSQLiteDriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, counter
}
func openCountingListMediaDB(t *testing.T) (*sql.DB, *atomic.Int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "count.sqlite")
	bootstrap, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bootstrap.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES(1,'lib','movie','E:/media',1);
		INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(1,'user','x','user',1,'selected'),(2,'admin','x','admin',1,'all');
		INSERT INTO user_library_permission(user_id,library_id) VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		if _, err = bootstrap.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type) VALUES(?,1,?,?,?,'video')`, i, fmt.Sprintf("f-%d", i), fmt.Sprintf("Media %d", i), fmt.Sprintf("E:/media/%d.mp4", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = bootstrap.Close(); err != nil {
		t.Fatal(err)
	}

	return openCountingSQLitePath(t, path)
}

func TestListMediaActualQueryCountIsConstantWithReturnedRows(t *testing.T) {
	db, counter := openCountingListMediaDB(t)
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	cases := []struct {
		name, role, username string
		userID               int64
		wantQueries          int64
	}{
		{name: "admin", role: "admin", username: "admin", userID: 2, wantQueries: 2},
		{name: "selected-user", role: "user", username: "user", userID: 1, wantQueries: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counts := make([]int64, 0, 2)
			for _, limit := range []int{1, 20} {
				counter.Store(0)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/media?limit=%d", limit), nil)
				setUserCtx(c, tc.userID, tc.role, tc.username)
				h.ListMedia(c)
				if w.Code != http.StatusOK {
					t.Fatalf("limit=%d status=%d body=%s", limit, w.Code, w.Body.String())
				}
				if got := len(responseMediaIDs(t, w)); got != limit {
					t.Fatalf("limit=%d rows=%d", limit, got)
				}
				counts = append(counts, counter.Load())
			}
			if counts[0] != counts[1] || counts[0] != tc.wantQueries {
				t.Fatalf("actual QueryContext+ExecContext counts for 1/20 rows=%v, want [%d %d]", counts, tc.wantQueries, tc.wantQueries)
			}
		})
	}
}
