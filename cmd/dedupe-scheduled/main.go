// One-off utility: dedupe scheduled_task rows in the Knox media database.
// Usage (from media/): go run ./cmd/dedupe-scheduled/ [path/to/knox-media.db]
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"knox-media/internal/store"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := filepath.Join("data", "knox-media.db")
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var before int
	if err := db.QueryRow(`SELECT COUNT(1) FROM scheduled_task`).Scan(&before); err != nil {
		fmt.Fprintf(os.Stderr, "count before: %v\n", err)
		os.Exit(1)
	}

	deleted, err := store.DedupeScheduledTasks(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dedupe: %v\n", err)
		os.Exit(1)
	}
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduled_task_type_name ON scheduled_task(task_type, name)`)

	var after int
	_ = db.QueryRow(`SELECT COUNT(1) FROM scheduled_task`).Scan(&after)
	fmt.Printf("scheduled_task: before=%d deleted=%d after=%d\n", before, deleted, after)

	rows, err := db.Query(`SELECT id, name, task_type, enabled FROM scheduled_task ORDER BY task_type, id`)
	if err != nil {
		os.Exit(0)
	}
	defer rows.Close()
	for rows.Next() {
		var id, enabled int64
		var name, taskType string
		if rows.Scan(&id, &name, &taskType, &enabled) == nil {
			fmt.Printf("  id=%d enabled=%d %s %s\n", id, enabled, taskType, name)
		}
	}
}
