package taskcontrol

import (
	"context"
	"testing"
)

func TestScrapeAndScanAdaptersListDetailAndActions(t *testing.T) {
	db, b := setupProjectionTestDB(t)
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE scrape_task(id INTEGER PRIMARY KEY AUTOINCREMENT,media_id INTEGER NOT NULL,status TEXT,fail_count INTEGER,retry_round INTEGER,priority INTEGER,available_at TIMESTAMP,created_at TIMESTAMP,started_at TIMESTAMP,finished_at TIMESTAMP,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP,message TEXT)`,
		`CREATE TABLE scan_task(id INTEGER PRIMARY KEY AUTOINCREMENT,library_id INTEGER,status TEXT,cancelled INTEGER DEFAULT 0,error_message TEXT,created_at TIMESTAMP,updated_at TIMESTAMP,started_at TIMESTAMP,finished_at TIMESTAMP)`,
		`CREATE TABLE scan_lease(library_id INTEGER PRIMARY KEY,scan_task_id INTEGER,owner_id TEXT,lease_until TIMESTAMP)`,
		`INSERT INTO media(id,library_id,title,file_path) VALUES(41,7,'Scrape media','/m/41')`,
		`INSERT INTO scrape_task(id,media_id,status,fail_count,retry_round,priority,available_at,created_at,started_at,generation,lease_owner,lease_until,message) VALUES(51,41,'running',2,1,4,datetime('now'),datetime('now'),datetime('now'),3,'scraper/51',datetime('now','+1 minute'),'')`,
		`INSERT INTO post_ingest_task(id,media_id,generation,task_type,status,created_at,updated_at) VALUES(52,41,3,'metadata','running',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		`INSERT INTO scan_task(id,library_id,status,error_message,created_at,updated_at,started_at) VALUES(61,7,'running','',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(7,61,'scan/61',datetime('now','+1 minute'))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	b.RegisterAdapter(NewScrapeAdapter(db))
	b.RegisterAdapter(NewScanAdapter(db))
	m := NewMutateService(db)
	m.SetExternalAbortHandler("scan_task", func(context.Context, int64) error { return nil })
	b.SetActionResolver(m.AllowedActions)
	q := NewQueryService(b)
	scrape, err := q.List(context.Background(), QueryFilter{TaskType: "metadata_scrape", Status: "running"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if scrape.Total != 2 || len(scrape.Items) != 2 {
		t.Fatalf("scrape total/items=%d/%d, want 2/2: %+v", scrape.Total, len(scrape.Items), scrape)
	}
	seen := map[string]int{}
	for _, item := range scrape.Items {
		seen[item.TaskID]++
	}
	if seen["scrape_task:51"] != 1 || seen["orchestration:52"] != 1 {
		t.Fatalf("merged metadata identities=%v, want each exactly once", seen)
	}
	for _, item := range scrape.Items {
		if item.SourceKind == "scrape_task" && (item.AllowedActions.Abort || item.AllowedActions.Reset) {
			t.Fatalf("unsafe scrape actions=%+v", item.AllowedActions)
		}
	}
	detail, err := q.Detail(context.Background(), "scrape_task:51")
	if err != nil || detail == nil || detail.Row.TaskType != "metadata_scrape" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	scan, err := q.List(context.Background(), QueryFilter{TaskType: "scan", Status: "running"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Total != 1 || len(scan.Items) != 1 || scan.Items[0].TaskID != "scan_task:61" || !scan.Items[0].AllowedActions.Abort || scan.Items[0].AllowedActions.Reset {
		t.Fatalf("scan=%+v", scan)
	}
}

func TestAvailableRegistryTypesResolveRegisteredSources(t *testing.T) {
	_, b := setupProjectionTestDB(t)
	b.RegisterAdapter(NewScrapeAdapter(b.DB()))
	b.RegisterAdapter(NewScanAdapter(b.DB()))
	kinds := b.RegisteredKinds()
	for _, g := range b.Registry().Groups {
		for _, spec := range g.Types {
			if !spec.Available {
				continue
			}
			ok := false
			for _, m := range spec.SourceMappings {
				if kinds[m.Kind] {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("available type %s has no registered source", spec.Type)
			}
		}
	}
}

func TestOrchestrationBackedTypesResolveWithoutSecondaryAdapters(t *testing.T) {
	db, b := setupProjectionTestDB(t)
	defer db.Close()
	cases := map[string][]string{
		"package":        {"package"},
		"preview":        {"preview"},
		"keyframe":       {"keyframe", "keyframe_extract"},
		"atrack_extract": {"atrack", "atrack_extract"},
	}
	q := NewQueryService(b)
	for publicType, internalTypes := range cases {
		spec := findSpec(t, b.Registry(), publicType)
		if !spec.Available {
			t.Errorf("%s must remain available", publicType)
		}
		sources := q.adaptersForPublicType(publicType)
		if len(sources) != len(internalTypes) {
			t.Fatalf("%s sources=%d want %d: %+v", publicType, len(sources), len(internalTypes), sources)
		}
		for i, internalType := range internalTypes {
			if sources[i].adapter.Kind() != "orchestration" || sources[i].mapping.InternalType != internalType {
				t.Errorf("%s source[%d]=%s/%s want orchestration/%s", publicType, i, sources[i].adapter.Kind(), sources[i].mapping.InternalType, internalType)
			}
			insertOracleTask(t, db, internalType, "waiting", nil)
		}
		result, err := q.List(context.Background(), QueryFilter{TaskType: publicType}, "", 10)
		if err != nil {
			t.Fatalf("list %s: %v", publicType, err)
		}
		if result.Total != int64(len(internalTypes)) {
			t.Errorf("%s total=%d want %d", publicType, result.Total, len(internalTypes))
		}
		for _, item := range result.Items {
			if item.SourceKind != "orchestration" {
				t.Errorf("%s resolved source=%s", publicType, item.SourceKind)
			}
		}
	}
}

func TestTranscodeAdapterRemoveCapabilitiesRespectLinkageAndStatus(t *testing.T) {
	db, b := setupProjectionTestDB(t)
	defer db.Close()
	for _, stmt := range []string{
		`INSERT INTO transcode_task(id,file_id,status,task_type,created_at) VALUES(71,'standalone-cancelled','cancelled','pretranscode',CURRENT_TIMESTAMP)`,
		`INSERT INTO transcode_task(id,file_id,status,task_type,ingest_run_id,ingest_step_id,generation,created_at) VALUES(72,'linked-cancelled','cancelled','pretranscode',1,2,1,CURRENT_TIMESTAMP)`,
		`INSERT INTO transcode_task(id,file_id,status,task_type,created_at) VALUES(73,'standalone-running','running','pretranscode',CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	b.RegisterAdapter(NewTranscodeAdapter(db))
	m := NewMutateService(db)
	m.SetExternalOperationHandler("transcode_task", "remove", func(context.Context, int64) error { return nil }, func(row *ProjectionRow) bool { return !row.Linked })
	b.SetActionResolver(m.AllowedActions)

	cases := []struct {
		id         string
		wantLinked bool
		wantRemove bool
	}{
		{"transcode_task:71", false, true},
		{"transcode_task:72", true, false},
		{"transcode_task:73", false, false},
	}
	for _, tc := range cases {
		row, err := b.Project(context.Background(), tc.id)
		if err != nil {
			t.Fatalf("project %s: %v", tc.id, err)
		}
		if row == nil || row.Linked != tc.wantLinked || row.AllowedActions.Remove != tc.wantRemove {
			t.Errorf("%s row=%+v, want linked=%v remove=%v", tc.id, row, tc.wantLinked, tc.wantRemove)
		}
	}
}
