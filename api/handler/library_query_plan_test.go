package handler

import (
	"strings"
	"testing"
)

func TestListLibrariesGroupedJoinPlanUsesLibraryIndexes(t *testing.T) {
	h := setupAccessTestDB(t)
	rows, err := h.App.DB.Query(`EXPLAIN QUERY PLAN
        SELECT l.id,COALESCE(mc.media_count,0),COALESCE(latest_scan.id,0)
        FROM library l
        LEFT JOIN (SELECT library_id,COUNT(*) AS media_count FROM media GROUP BY library_id) mc ON mc.library_id=l.id
        LEFT JOIN (SELECT st.* FROM scan_task st JOIN (SELECT library_id,MAX(id) AS max_id FROM scan_task GROUP BY library_id) latest ON latest.max_id=st.id) latest_scan ON latest_scan.library_id=l.id
        ORDER BY l.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(details, "\n")
	for _, index := range []string{"idx_media_library", "idx_scan_task_library"} {
		if !strings.Contains(joined, index) {
			t.Fatalf("plan missing %s:\n%s", index, joined)
		}
	}
}
