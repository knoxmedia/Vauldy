package handler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"knox-media/internal/store"
	modernsqlite "modernc.org/sqlite"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

const writeCountingSQLiteDriverName = "sqlite-relationship-get-write-counting"

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

func (c *writeCountingConn) Begin() (driver.Tx, error) { c.writes.Add(1); return c.Conn.Begin() }
func (c *writeCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.writes.Add(1)
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return b.BeginTx(ctx, opts)
}
func (c *writeCountingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.writes.Add(1)
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, q, args)
}
func (c *writeCountingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	x, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return x.QueryContext(ctx, q, args)
}
func openPhotoGETWriteCountingDB(t *testing.T) (*sql.DB, *atomic.Int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relationship-get.sqlite")
	bootstrap, err := store.OpenSQLite(path)
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
func posterHandlerTestDB(t *testing.T) (*sql.DB, int64) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "relationship-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lr, err := db.Exec(`INSERT INTO library(name,type,path,image_providers) VALUES('test','movie',?,'embedded')`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := lr.LastInsertId()
	mr, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status,meta_json) VALUES(?,'test-media','movie.mp4','movie','video','active','{}')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := mr.LastInsertId()
	return db, mid
}
