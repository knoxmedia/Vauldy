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
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		fmt.Println("ping:", err)
		os.Exit(1)
	}

	queries := []struct {
		name string
		q    string
	}{
		{"v1 processing runs (ReplaceActiveV1Runs candidates)", `SELECT COUNT(*) FROM media_ingest_run WHERE COALESCE(policy_version,1)=1 AND status='processing' AND superseded_at IS NULL`},
		{"v1 runs not superseded (any status)", `SELECT COUNT(*) FROM media_ingest_run WHERE COALESCE(policy_version,1)=1 AND superseded_at IS NULL`},
		{"current policy v2/v3 runs", `SELECT COUNT(*) FROM media_ingest_run WHERE policy_version IN (2,3) AND superseded_at IS NULL`},
		{"active scheduler reservations", `SELECT COUNT(*) FROM scheduler_reservation WHERE status='active'`},
		{"active reservations with NULL lease", `SELECT COUNT(*) FROM scheduler_reservation WHERE status='active' AND lease_until IS NULL`},
		{"active reservations expired", `SELECT COUNT(*) FROM scheduler_reservation WHERE status='active' AND lease_until IS NOT NULL AND lease_until < datetime('now')`},
		{"running post_ingest_task", `SELECT COUNT(*) FROM post_ingest_task WHERE status='running'`},
		{"waiting post_ingest_task", `SELECT COUNT(*) FROM post_ingest_task WHERE status='waiting'`},
		{"failed post_ingest_task", `SELECT COUNT(*) FROM post_ingest_task WHERE status='failed'`},
		{"running scrape_task", `SELECT COUNT(*) FROM scrape_task WHERE status='running'`},
		{"waiting scrape_task", `SELECT COUNT(*) FROM scrape_task WHERE status='waiting'`},
		{"running transcode_task", `SELECT COUNT(*) FROM transcode_task WHERE status='running'`},
		{"steps waiting/running", `SELECT COUNT(*) FROM media_ingest_step WHERE status IN ('waiting','running')`},
		{"media total", `SELECT COUNT(*) FROM media`},
		{"media processing publication_state", `SELECT COUNT(*) FROM media WHERE publication_state='processing'`},
	}
	for _, q := range queries {
		var n int
		if err := db.QueryRowContext(ctx, q.q).Scan(&n); err != nil {
			fmt.Printf("ERR %s: %v\n", q.name, err)
			continue
		}
		fmt.Printf("%-55s = %d\n", q.name, n)
	}

	// Sample v1 processing runs
	rows, err := db.QueryContext(ctx, `SELECT r.id, r.media_id, r.generation, r.status, COALESCE(r.policy_version,1), m.ingest_generation FROM media_ingest_run r LEFT JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE COALESCE(r.policy_version,1)=1 AND r.status='processing' AND r.superseded_at IS NULL ORDER BY r.id LIMIT 20`)
	if err == nil {
		fmt.Println("\nv1 processing runs detail:")
		for rows.Next() {
			var id, mediaID, gen, ingestGen int64
			var status string
			var pol int
			if err := rows.Scan(&id, &mediaID, &gen, &status, &pol, &ingestGen); err != nil {
				fmt.Println("scan:", err)
				break
			}
			fmt.Printf("  run=%d media=%d gen=%d ingest_gen=%d status=%s policy=%d\n", id, mediaID, gen, status, pol, ingestGen)
		}
		rows.Close()
	}

	// Distribution of current-policy run statuses
	fmt.Println("\ncurrent-policy run status distribution:")
	rrows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM media_ingest_run WHERE policy_version IN (2,3) AND superseded_at IS NULL GROUP BY status ORDER BY status`)
	if err == nil {
		for rrows.Next() {
			var st string
			var n int
			if err := rrows.Scan(&st, &n); err != nil {
				fmt.Println("scan:", err)
				break
			}
			fmt.Printf("  status=%-12s count=%d\n", st, n)
		}
		rrows.Close()
	}

	// Steps per run (max)
	fmt.Println("\nsteps per current run (top 10):")
	srows, err := db.QueryContext(ctx, `SELECT run_id, COUNT(*) AS n FROM media_ingest_step WHERE run_id IN (SELECT id FROM media_ingest_run WHERE policy_version IN (2,3) AND superseded_at IS NULL) GROUP BY run_id ORDER BY n DESC LIMIT 10`)
	if err == nil {
		for srows.Next() {
			var rid, n int64
			if err := srows.Scan(&rid, &n); err != nil {
				fmt.Println("scan:", err)
				break
			}
			fmt.Printf("  run=%d steps=%d\n", rid, n)
		}
		srows.Close()
	}

	// Steps in terminal (failed/cancelled) states per run, count runs with failed/cancelled steps
	fmt.Println("\nruns with failed/cancelled steps:")
	crows, err := db.QueryContext(ctx, `SELECT r.id, SUM(s.status='failed') AS f, SUM(s.status='cancelled') AS c, SUM(s.status='waiting') AS w, SUM(s.status='running') AS rn FROM media_ingest_run r JOIN media_ingest_step s ON s.run_id=r.id WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL GROUP BY r.id HAVING f>0 OR c>0 OR w>0 OR rn>0 ORDER BY r.id LIMIT 30`)
	if err == nil {
		for crows.Next() {
			var rid, f, c, w, rn int64
			if err := crows.Scan(&rid, &f, &c, &w, &rn); err != nil {
				fmt.Println("scan:", err)
				break
			}
			fmt.Printf("  run=%d failed=%d cancelled=%d waiting=%d running=%d\n", rid, f, c, w, rn)
		}
		crows.Close()
	}
}
