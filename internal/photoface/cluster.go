package photoface

import (
	"database/sql"
	"fmt"
)

type personCandidate struct {
	ID        int64
	Embedding []float32
	FaceCount int
}

// AssignPerson links a detected face to an existing or new person cluster.
func AssignPerson(db *sql.DB, libraryID, faceID int64, embedding []float32, threshold float32) error {
	if db == nil || libraryID <= 0 || faceID <= 0 || len(embedding) == 0 {
		return fmt.Errorf("invalid assign args")
	}
	embedding = normalizeEmbedding(embedding)

	candidates, err := loadPersonCandidates(db, libraryID)
	if err != nil {
		return err
	}

	var bestID int64
	var bestSim float32
	for _, c := range candidates {
		if sim := cosineSimilarity(embedding, c.Embedding); sim > bestSim {
			bestSim = sim
			bestID = c.ID
		}
	}

	if bestID > 0 && bestSim >= threshold {
		return attachFaceToPerson(db, libraryID, faceID, bestID, embedding, bestSim)
	}
	return createPersonForFace(db, libraryID, faceID, embedding)
}

func loadPersonCandidates(db *sql.DB, libraryID int64) ([]personCandidate, error) {
	rows, err := db.Query(`
		SELECT id, embedding, face_count
		FROM photo_person
		WHERE library_id = ? AND embedding IS NOT NULL
	`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []personCandidate
	for rows.Next() {
		var id int64
		var faceCount int
		var blob []byte
		if rows.Scan(&id, &blob, &faceCount) != nil {
			continue
		}
		out = append(out, personCandidate{ID: id, Embedding: unpackEmbedding(blob), FaceCount: faceCount})
	}
	return out, nil
}

func attachFaceToPerson(db *sql.DB, libraryID, faceID, personID int64, embedding []float32, score float32) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var prevBlob []byte
	var prevCount int
	if err := tx.QueryRow(`SELECT embedding, face_count FROM photo_person WHERE id = ? AND library_id = ?`, personID, libraryID).Scan(&prevBlob, &prevCount); err != nil {
		return err
	}
	centroid := mergeCentroid(unpackEmbedding(prevBlob), prevCount, embedding)
	newCount := prevCount + 1

	if _, err := tx.Exec(`
		UPDATE photo_face SET person_id = ?, match_score = ? WHERE id = ?`,
		personID, score, faceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE photo_person SET embedding = ?, face_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		packEmbedding(centroid), newCount, personID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE photo_person SET media_count = (
			SELECT COUNT(DISTINCT media_id) FROM photo_face WHERE person_id = ?
		) WHERE id = ?`, personID, personID); err != nil {
		return err
	}
	var cover sql.NullInt64
	_ = tx.QueryRow(`SELECT cover_face_id FROM photo_person WHERE id = ?`, personID).Scan(&cover)
	if !cover.Valid || cover.Int64 <= 0 {
		_, _ = tx.Exec(`UPDATE photo_person SET cover_face_id = ? WHERE id = ?`, faceID, personID)
	}
	return tx.Commit()
}

func createPersonForFace(db *sql.DB, libraryID, faceID int64, embedding []float32) error {
	var nextLabel int
	_ = db.QueryRow(`SELECT COUNT(1) FROM photo_person WHERE library_id = ?`, libraryID).Scan(&nextLabel)
	nextLabel++
	label := fmt.Sprintf("人物 %d", nextLabel)

	res, err := db.Exec(`
		INSERT INTO photo_person (library_id, label, cover_face_id, face_count, media_count, embedding, updated_at)
		VALUES (?, ?, ?, 1, 1, ?, CURRENT_TIMESTAMP)`,
		libraryID, label, faceID, packEmbedding(normalizeEmbedding(embedding)))
	if err != nil {
		return err
	}
	personID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE photo_face SET person_id = ?, match_score = 1 WHERE id = ?`, personID, faceID)
	return err
}

// ReclusterLibrary clears person assignments and rebuilds clusters for a library.
func ReclusterLibrary(db *sql.DB, libraryID int64, threshold float32) error {
	if db == nil || libraryID <= 0 {
		return fmt.Errorf("invalid library id")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM photo_person WHERE library_id = ?`, libraryID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE photo_face SET person_id = NULL, match_score = NULL
		WHERE library_id = ?`, libraryID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	rows, err := db.Query(`
		SELECT id, embedding FROM photo_face
		WHERE library_id = ? AND embedding IS NOT NULL
		ORDER BY quality DESC, id ASC`, libraryID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var faceID int64
		var blob []byte
		if rows.Scan(&faceID, &blob) != nil {
			continue
		}
		if err := AssignPerson(db, libraryID, faceID, unpackEmbedding(blob), threshold); err != nil {
			return err
		}
	}
	return nil
}

func floats64To32(v []float64) []float32 {
	if len(v) == 0 {
		return nil
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x)
	}
	return out
}
