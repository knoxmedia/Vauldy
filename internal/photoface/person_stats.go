package photoface

import (
	"context"
	"database/sql"
)

// RefreshPersonStatsTx recomputes person aggregates from actual face relations.
func RefreshPersonStatsTx(ctx context.Context, tx *sql.Tx, personIDs []int64) error {
	seen := map[int64]struct{}{}
	for _, personID := range personIDs {
		if personID <= 0 {
			continue
		}
		if _, ok := seen[personID]; ok {
			continue
		}
		seen[personID] = struct{}{}
		var count, mediaCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(DISTINCT media_id) FROM photo_face WHERE person_id=?`, personID).Scan(&count, &mediaCount); err != nil {
			return err
		}
		if count == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM photo_person WHERE id=?`, personID); err != nil {
				return err
			}
			continue
		}
		var cover int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM photo_face WHERE person_id=? ORDER BY quality DESC,id ASC LIMIT 1`, personID).Scan(&cover); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT embedding FROM photo_face WHERE person_id=? AND embedding IS NOT NULL ORDER BY id`, personID)
		if err != nil {
			return err
		}
		var centroid []float32
		n := 0
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return err
			}
			centroid = mergeCentroid(centroid, n, unpackEmbedding(b))
			n++
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if _, err = tx.ExecContext(ctx, `UPDATE photo_person SET face_count=?,media_count=?,cover_face_id=?,embedding=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, count, mediaCount, cover, packEmbedding(centroid), personID); err != nil {
			return err
		}
	}
	return ctx.Err()
}
