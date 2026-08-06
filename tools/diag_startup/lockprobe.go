package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := "K:/Release/vauldy_windows_amd64/data/knox-media.db"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)")
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()

	// Try to begin an IMMEDIATE transaction (write lock).
	start := time.Now()
	conn, err := db.Conn(ctx)
	if err != nil {
		fmt.Println("conn:", err)
		os.Exit(1)
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	if err != nil {
		fmt.Printf("BEGIN IMMEDIATE failed after %v: %v\n", time.Since(start), err)
		os.Exit(1)
	}
	fmt.Printf("BEGIN IMMEDIATE succeeded in %v\n", time.Since(start))
	_, _ = conn.ExecContext(ctx, `COMMIT`)
	fmt.Println("done")
}
