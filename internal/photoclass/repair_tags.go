package photoclass

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// RepairLibraryPhotoTags rewrites garbled photo tags in meta_json to canonical UTF-8 names.
func RepairLibraryPhotoTags(db *sql.DB, libraryID int64) (int, error) {
	if db == nil {
		return 0, nil
	}
	q := `SELECT id, COALESCE(meta_json,'') FROM media WHERE file_type='image' AND status='active'`
	args := []any{}
	if libraryID > 0 {
		q += ` AND library_id = ?`
		args = append(args, libraryID)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type photoMeta struct {
		Tags   []string `json:"tags"`
		AITags []string `json:"ai_tags"`
	}
	type root struct {
		Photo photoMeta `json:"photo"`
	}

	updated := 0
	for rows.Next() {
		var id int64
		var metaJSON string
		if rows.Scan(&id, &metaJSON) != nil {
			continue
		}
		metaJSON = strings.TrimSpace(metaJSON)
		if metaJSON == "" {
			continue
		}
		var root root
		if err := json.Unmarshal([]byte(metaJSON), &root); err != nil {
			continue
		}
		newTags := NormalizeTags(root.Photo.Tags)
		newAI := NormalizeTags(root.Photo.AITags)
		if tagsEqual(newTags, root.Photo.Tags) && tagsEqual(newAI, root.Photo.AITags) {
			continue
		}
		merged, err := mergePhotoTagsJSON(metaJSON, newTags, newAI)
		if err != nil {
			continue
		}
		if _, err := db.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, merged, id); err != nil {
			continue
		}
		updated++
	}
	return updated, nil
}

func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mergePhotoTagsJSON(metaJSON string, tags, aiTags []string) (string, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &root); err != nil {
		return "", err
	}
	photo, _ := root["photo"].(map[string]any)
	if photo == nil {
		photo = map[string]any{}
		root["photo"] = photo
	}
	photo["tags"] = tags
	if len(aiTags) > 0 {
		photo["ai_tags"] = aiTags
	}
	out, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
