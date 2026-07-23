package publication

import "fmt"

// LinkedClaimEligibilitySQL returns the canonical predicate shared by linked
// publication queue claims. Completely unlinked legacy rows remain eligible;
// partial or drifted publication identity fails closed.
func LinkedClaimEligibilitySQL(alias string) string {
	return fmt.Sprintf(`(
		(%[1]s.ingest_run_id IS NULL AND %[1]s.ingest_step_id IS NULL)
		OR
		(%[1]s.ingest_run_id IS NOT NULL AND %[1]s.ingest_step_id IS NOT NULL AND %[1]s.generation IS NOT NULL
		 AND EXISTS (
			SELECT 1 FROM media_ingest_run r
			JOIN media m ON m.id=r.media_id
			JOIN media_ingest_step st ON st.id=%[1]s.ingest_step_id
			WHERE r.id=%[1]s.ingest_run_id AND r.media_id=%[1]s.media_id
			  AND r.generation=%[1]s.generation AND m.id=%[1]s.media_id
			  AND m.ingest_generation=%[1]s.generation
			  AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL
			  AND r.status IN ('processing','published','degraded')
			  AND st.run_id=r.id AND st.media_id=r.media_id AND st.generation=r.generation
			  AND st.status='waiting'
			  AND NOT EXISTS (
				SELECT 1 FROM media_ingest_step_dependency d
				LEFT JOIN media_ingest_step dep ON dep.id=d.depends_on_step_id
				WHERE d.step_id=st.id AND NOT (
				  (d.dependency_kind='step_done' AND d.depends_on_step_id IS NOT NULL
				   AND dep.id IS NOT NULL AND dep.run_id=st.run_id AND dep.media_id=st.media_id
				   AND dep.generation=st.generation AND dep.status IN ('done','skipped'))
				  OR
				  (d.dependency_kind='media_visible' AND d.depends_on_step_id IS NULL
				   AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL)
				)
			  )
		 ))
	)`, alias)
}
