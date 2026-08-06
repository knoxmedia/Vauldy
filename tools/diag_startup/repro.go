package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"time"

	_ "modernc.org/sqlite"

	"knox-media/internal/publication"
)

func main() {
	dbPath := "E:/Projects/PowerCOM/Knox/media/tools/diag_startup/tmp/copy.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// Match production connection parameters (busy_timeout=30000 etc).
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	// Reset the snapshot first so repeated runs exercise the same path.
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		fmt.Println("checkpoint:", err)
		os.Exit(1)
	}

	// Run the exact startup validation path.
	fmt.Println("starting ValidateAggregateCurrentPolicy...")
	done := make(chan error, 1)
	go func() {
		// Give a way to inspect goroutine stacks if it hangs.
		done <- publication.ValidateAggregateCurrentPolicy(ctx, db)
	}()
	select {
	case err := <-done:
		if err != nil {
			fmt.Println("ValidateAggregateCurrentPolicy error:", err)
		} else {
			fmt.Println("ValidateAggregateCurrentPolicy OK")
		}
	case <-ctx.Done():
		fmt.Println("TIMEOUT/HANG detected. Stack dump:")
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		fmt.Println(string(buf[:n]))
		os.Exit(2)
	}
}
