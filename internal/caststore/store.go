package caststore

import (
	"context"
	"database/sql"
	"encoding/json"
	"knox-media/internal/store"
	"strings"
	"time"

	"knox-media/internal/musicparse"
)

const mediaPersonMediaSelect = `
			COALESCE(m.title,''),
			COALESCE(
				CAST(NULLIF(json_extract(m.meta_json, '$.scrape.year'), '') AS INTEGER),
				CAST(NULLIF(json_extract(m.meta_json, '$.year'), '') AS INTEGER),
				CAST(substr(COALESCE(
					NULLIF(json_extract(m.meta_json, '$.scrape.release_date'), ''),
					NULLIF(json_extract(m.meta_json, '$.release_date'), '')
				), 1, 4) AS INTEGER),
				0
			),
			COALESCE(
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''),
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.poster')), '')
			)`

const mediaPersonMediaOrder = `
		ORDER BY COALESCE(
			CAST(NULLIF(json_extract(m.meta_json, '$.scrape.year'), '') AS INTEGER),
			CAST(NULLIF(json_extract(m.meta_json, '$.year'), '') AS INTEGER),
			CAST(substr(COALESCE(
				NULLIF(json_extract(m.meta_json, '$.scrape.release_date'), ''),
				NULLIF(json_extract(m.meta_json, '$.release_date'), '')
			), 1, 4) AS INTEGER),
			0
		) DESC, m.title COLLATE NOCASE ASC`

// Person is a cast/crew member record.
type Person struct {
	ID          int64
	Name        string
	NameNorm    string
	EnglishName string
	Gender      int
	BirthDate   string
	BirthPlace  string
	Nationality string
	Occupations []string
	Biography   string
	AvatarURL   string
	Aliases     string
	Scraped     bool
	ScrapedAt   string
	TMDBID      string
	IMDBID      string
	DoubanID    string
	FieldLocks  map[string]bool
	WorkCount   int64
	CreatedAt   string
	UpdatedAt   string
}

// MediaPersonLink is a filmography / cast credit row.
type MediaPersonLink struct {
	ID            int64
	MediaID       int64
	PersonID      int64
	PersonName    string
	AvatarURL     string
	Occupation    string
	CharacterName string
	RoleType      string
	SortOrder     int
	MediaTitle    string
	MediaYear     int64
	PosterURL     string
}

// Collaborator is a co-star summary.
type Collaborator struct {
	PersonID           int64
	Name               string
	AvatarURL          string
	CollaborationCount int64
	RecentMovieTitles  []string
}

// PersonPatch holds editable person fields.
type PersonPatch struct {
	Name        string
	EnglishName string
	Gender      *int
	BirthDate   string
	BirthPlace  string
	Nationality string
	Occupations []string
	Biography   string
	AvatarURL   string
	Aliases     string
	TMDBID      string
	IMDBID      string
	DoubanID    string
	Scraped     *bool
}

func normName(name string) string {
	return musicparse.NormKey(strings.TrimSpace(name))
}

// NormPersonName returns the normalized lookup key for a person name.
func NormPersonName(name string) string {
	return normName(name)
}

func encodeOccupations(list []string) string {
	if len(list) == 0 {
		return "[]"
	}
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, o := range list {
		o = strings.TrimSpace(o)
		if o == "" || !ValidOccupations[o] || seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func decodeOccupations(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func decodeFieldLocks(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return map[string]bool{}
	}
	var out map[string]bool
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]bool{}
	}
	return out
}

func encodeFieldLocks(m map[string]bool) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// FindOrCreateByName returns an existing person or creates a stub.
func FindOrCreateByName(db *sql.DB, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || db == nil {
		return 0, sql.ErrNoRows
	}
	norm := normName(name)
	var id int64
	err := db.QueryRow(`
		SELECT id FROM cast_person
		WHERE name_norm = ? AND deleted_at IS NULL
		LIMIT 1
	`, norm).Scan(&id)
	if err == nil && id > 0 {
		return id, nil
	}
	res, err := db.Exec(`
		INSERT INTO cast_person (name, name_norm, occupation_json)
		VALUES (?, ?, '[]')
	`, name, norm)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FindOrCreateByTMDB returns person by TMDB id or creates one.
func FindOrCreateByTMDB(db *sql.DB, tmdbID, name string) (int64, error) {
	tmdbID = strings.TrimSpace(tmdbID)
	if tmdbID == "" || db == nil {
		return FindOrCreateByName(db, name)
	}
	var id int64
	err := db.QueryRow(`
		SELECT id FROM cast_person
		WHERE tmdb_id = ? AND deleted_at IS NULL
		LIMIT 1
	`, tmdbID).Scan(&id)
	if err == nil && id > 0 {
		return id, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unknown"
	}
	res, err := db.Exec(`
		INSERT INTO cast_person (name, name_norm, tmdb_id, occupation_json)
		VALUES (?, ?, ?, '[]')
	`, name, normName(name), tmdbID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LinkMediaPerson associates a person with a media item.
func LinkMediaPerson(db *sql.DB, mediaID, personID int64, occupation, characterName, roleType string, sortOrder int) error {
	if db == nil || mediaID <= 0 || personID <= 0 {
		return nil
	}
	occupation = strings.TrimSpace(occupation)
	if occupation == "" {
		occupation = OccActor
	}
	if !ValidOccupations[occupation] {
		occupation = OccOther
	}
	if sortOrder <= 0 {
		sortOrder = 9999
	}
	_, err := db.Exec(`
		INSERT INTO media_person (media_id, person_id, occupation, character_name, role_type, sort_order)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(media_id, person_id, occupation) DO UPDATE SET
			character_name = excluded.character_name,
			role_type = excluded.role_type,
			sort_order = excluded.sort_order,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID, personID, occupation, strings.TrimSpace(characterName), strings.TrimSpace(roleType), sortOrder)
	return err
}

// MergePersonOccupations adds occupation tags to a person record.
func MergePersonOccupations(db *sql.DB, personID int64, occupations ...string) {
	if db == nil || personID <= 0 || len(occupations) == 0 {
		return
	}
	var raw sql.NullString
	if err := db.QueryRow(`SELECT occupation_json FROM cast_person WHERE id = ?`, personID).Scan(&raw); err != nil {
		return
	}
	existing := decodeOccupations(raw.String)
	seen := map[string]bool{}
	for _, o := range existing {
		seen[o] = true
	}
	for _, o := range occupations {
		o = strings.TrimSpace(o)
		if o != "" && ValidOccupations[o] && !seen[o] {
			seen[o] = true
			existing = append(existing, o)
		}
	}
	_, _ = db.Exec(`UPDATE cast_person SET occupation_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		encodeOccupations(existing), personID)
}

// ApplyScrapePatch updates person fields respecting field locks.
func ApplyScrapePatch(db *sql.DB, personID int64, patch PersonPatch, lockFields ...string) error {
	if db == nil || personID <= 0 {
		return nil
	}
	var locksRaw sql.NullString
	var existingName sql.NullString
	if err := db.QueryRow(`SELECT name, field_locks_json FROM cast_person WHERE id = ? AND deleted_at IS NULL`, personID).
		Scan(&existingName, &locksRaw); err != nil {
		return err
	}
	locks := decodeFieldLocks(locksRaw.String)
	for _, f := range lockFields {
		if f = strings.TrimSpace(f); f != "" {
			locks[f] = true
		}
	}
	set := func(field string, val any) (string, any) {
		if locks[field] {
			return "", nil
		}
		return field, val
	}
	updates := []string{"updated_at = CURRENT_TIMESTAMP", "scraped = 1", "scraped_at = CURRENT_TIMESTAMP"}
	args := []any{}
	if v := strings.TrimSpace(patch.Name); v != "" {
		if f, a := set("name", v); f != "" {
			updates = append(updates, "name = ?", "name_norm = ?")
			args = append(args, a, normName(v))
		}
	}
	if f, a := set("english_name", strings.TrimSpace(patch.EnglishName)); f != "" && a != "" {
		updates = append(updates, "english_name = ?")
		args = append(args, a)
	}
	if patch.Gender != nil {
		if f, a := set("gender", *patch.Gender); f != "" {
			updates = append(updates, "gender = ?")
			args = append(args, a)
		}
	}
	if f, a := set("birth_date", strings.TrimSpace(patch.BirthDate)); f != "" && a != "" {
		updates = append(updates, "birth_date = ?")
		args = append(args, a)
	}
	if f, a := set("birth_place", strings.TrimSpace(patch.BirthPlace)); f != "" && a != "" {
		updates = append(updates, "birth_place = ?")
		args = append(args, a)
	}
	if f, a := set("nationality", strings.TrimSpace(patch.Nationality)); f != "" && a != "" {
		updates = append(updates, "nationality = ?")
		args = append(args, a)
	}
	if len(patch.Occupations) > 0 {
		if !locks["occupation"] {
			updates = append(updates, "occupation_json = ?")
			args = append(args, encodeOccupations(patch.Occupations))
		}
	}
	if f, a := set("biography", strings.TrimSpace(patch.Biography)); f != "" && a != "" {
		updates = append(updates, "biography = ?")
		args = append(args, a)
	}
	if f, a := set("avatar_url", strings.TrimSpace(patch.AvatarURL)); f != "" && a != "" {
		updates = append(updates, "avatar_url = ?")
		args = append(args, a)
	}
	if f, a := set("aliases", strings.TrimSpace(patch.Aliases)); f != "" && a != "" {
		updates = append(updates, "aliases = ?")
		args = append(args, a)
	}
	if f, a := set("tmdb_id", strings.TrimSpace(patch.TMDBID)); f != "" && a != "" {
		updates = append(updates, "tmdb_id = ?")
		args = append(args, a)
	}
	if f, a := set("imdb_id", strings.TrimSpace(patch.IMDBID)); f != "" && a != "" {
		updates = append(updates, "imdb_id = ?")
		args = append(args, a)
	}
	if f, a := set("douban_id", strings.TrimSpace(patch.DoubanID)); f != "" && a != "" {
		updates = append(updates, "douban_id = ?")
		args = append(args, a)
	}
	updates = append(updates, "field_locks_json = ?")
	args = append(args, encodeFieldLocks(locks))
	args = append(args, personID)
	q := "UPDATE cast_person SET " + strings.Join(updates, ", ") + " WHERE id = ?"
	_, err := db.Exec(q, args...)
	return err
}

// UpdatePerson applies a user edit and locks touched fields.
func UpdatePerson(db *sql.DB, personID int64, patch PersonPatch) error {
	if db == nil || personID <= 0 {
		return sql.ErrNoRows
	}
	name := strings.TrimSpace(patch.Name)
	if name == "" {
		return sql.ErrNoRows
	}
	locks := []string{"name", "english_name", "gender", "birth_date", "birth_place", "nationality",
		"occupation", "biography", "avatar_url", "aliases"}
	patch.Name = name
	if patch.Gender == nil {
		g := 0
		patch.Gender = &g
	}
	return ApplyScrapePatch(db, personID, patch, locks...)
}

// SoftDeletePerson marks a person deleted and removes media links.
func SoftDeletePerson(db *sql.DB, personID int64, removeLinks bool) error {
	if db == nil || personID <= 0 {
		return nil
	}
	if removeLinks {
		_, _ = db.Exec(`DELETE FROM media_person WHERE person_id = ?`, personID)
	}
	_, err := db.Exec(`
		UPDATE cast_person SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, personID)
	return err
}

// CountMediaLinks returns how many media items reference this person.
func CountMediaLinks(db *sql.DB, personID int64) (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT COUNT(1) FROM media_person WHERE person_id = ?`, personID).Scan(&n)
	return n, err
}

func scanPerson(row scanner) (*Person, error) {
	var p Person
	var occRaw, locksRaw, scrapedAt, deletedAt sql.NullString
	var scraped int64
	if err := row.Scan(
		&p.ID, &p.Name, &p.NameNorm, &p.EnglishName, &p.Gender,
		&p.BirthDate, &p.BirthPlace, &p.Nationality, &occRaw,
		&p.Biography, &p.AvatarURL, &p.Aliases, &scraped, &scrapedAt,
		&p.TMDBID, &p.IMDBID, &p.DoubanID, &locksRaw,
		&p.CreatedAt, &p.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	p.Occupations = decodeOccupations(occRaw.String)
	p.FieldLocks = decodeFieldLocks(locksRaw.String)
	p.Scraped = scraped > 0
	p.ScrapedAt = scrapedAt.String
	return &p, nil
}

type scanner interface {
	Scan(dest ...any) error
}

const personSelectCols = `
	id, name, name_norm, COALESCE(english_name,''), COALESCE(gender,0),
	COALESCE(birth_date,''), COALESCE(birth_place,''), COALESCE(nationality,''),
	COALESCE(occupation_json,'[]'), COALESCE(biography,''), COALESCE(avatar_url,''),
	COALESCE(aliases,''), COALESCE(scraped,0), scraped_at,
	COALESCE(tmdb_id,''), COALESCE(imdb_id,''), COALESCE(douban_id,''),
	COALESCE(field_locks_json,'{}'), created_at, updated_at, deleted_at
`

// GetPerson loads one person by id.
func GetPerson(db *sql.DB, personID int64) (*Person, error) {
	row := db.QueryRow(`SELECT `+personSelectCols+` FROM cast_person WHERE id = ? AND deleted_at IS NULL`, personID)
	p, err := scanPerson(row)
	if err != nil {
		return nil, err
	}
	_ = db.QueryRow(`SELECT COUNT(DISTINCT media_id) FROM media_person WHERE person_id = ?`, personID).Scan(&p.WorkCount)
	return p, nil
}

// ListPersonsOptions filters person list queries.
type ListPersonsOptions struct {
	Search     string
	Occupation string
	Scraped    string // "yes" | "no" | ""
	Sort       string // name | works | created
	Page       int
	PageSize   int
}

// ListPersons returns paginated persons.
func ListPersons(db *sql.DB, opt ListPersonsOptions) ([]Person, int64, error) {
	if db == nil {
		return nil, 0, nil
	}
	if opt.Page <= 0 {
		opt.Page = 1
	}
	if opt.PageSize <= 0 {
		opt.PageSize = 48
	}
	if opt.PageSize > 200 {
		opt.PageSize = 200
	}
	where := `WHERE p.deleted_at IS NULL`
	args := []any{}
	if q := strings.TrimSpace(opt.Search); q != "" {
		like := "%" + q + "%"
		where += ` AND (p.name LIKE ? OR p.english_name LIKE ? OR p.aliases LIKE ?)`
		args = append(args, like, like, like)
	}
	if occ := strings.TrimSpace(opt.Occupation); occ != "" && ValidOccupations[occ] {
		where += ` AND p.occupation_json LIKE ?`
		args = append(args, "%\""+occ+"\"%")
	}
	switch strings.ToLower(strings.TrimSpace(opt.Scraped)) {
	case "yes":
		where += ` AND p.scraped = 1`
	case "no":
		where += ` AND COALESCE(p.scraped, 0) = 0`
	}
	var total int64
	countQ := `SELECT COUNT(1) FROM cast_person p ` + where
	if err := db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := `ORDER BY p.name COLLATE NOCASE ASC`
	switch strings.ToLower(strings.TrimSpace(opt.Sort)) {
	case "works":
		order = `ORDER BY work_count DESC, p.name COLLATE NOCASE ASC`
	case "created":
		order = `ORDER BY p.created_at DESC`
	}
	offset := (opt.Page - 1) * opt.PageSize
	q := `
		SELECT p.id, p.name, p.name_norm, COALESCE(p.english_name,''), COALESCE(p.gender,0),
			COALESCE(p.birth_date,''), COALESCE(p.birth_place,''), COALESCE(p.nationality,''),
			COALESCE(p.occupation_json,'[]'), COALESCE(p.biography,''), COALESCE(p.avatar_url,''),
			COALESCE(p.aliases,''), COALESCE(p.scraped,0), p.scraped_at,
			COALESCE(p.tmdb_id,''), COALESCE(p.imdb_id,''), COALESCE(p.douban_id,''),
			COALESCE(p.field_locks_json,'{}'), p.created_at, p.updated_at, p.deleted_at,
			(SELECT COUNT(DISTINCT mp.media_id) FROM media_person mp WHERE mp.person_id = p.id) AS work_count
		FROM cast_person p
		` + where + `
		` + order + `
		LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), opt.PageSize, offset)
	rows, err := db.Query(q, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Person, 0)
	for rows.Next() {
		var p Person
		var occRaw, locksRaw, scrapedAt, deletedAt sql.NullString
		var scraped int64
		if err := rows.Scan(
			&p.ID, &p.Name, &p.NameNorm, &p.EnglishName, &p.Gender,
			&p.BirthDate, &p.BirthPlace, &p.Nationality, &occRaw,
			&p.Biography, &p.AvatarURL, &p.Aliases, &scraped, &scrapedAt,
			&p.TMDBID, &p.IMDBID, &p.DoubanID, &locksRaw,
			&p.CreatedAt, &p.UpdatedAt, &deletedAt, &p.WorkCount,
		); err != nil {
			continue
		}
		p.Occupations = decodeOccupations(occRaw.String)
		p.FieldLocks = decodeFieldLocks(locksRaw.String)
		p.Scraped = scraped > 0
		p.ScrapedAt = scrapedAt.String
		out = append(out, p)
	}
	return out, total, nil
}

// ListMediaPersons returns cast/crew for a media item grouped data.
func ListMediaPersons(db *sql.DB, mediaID int64) ([]MediaPersonLink, error) {
	rows, err := db.Query(`
		SELECT mp.id, mp.media_id, mp.person_id, p.name, COALESCE(p.avatar_url,''),
			mp.occupation, COALESCE(mp.character_name,''), COALESCE(mp.role_type,''), mp.sort_order,`+mediaPersonMediaSelect+`
		FROM media_person mp
		JOIN cast_person p ON p.id = mp.person_id AND p.deleted_at IS NULL
		JOIN media m ON m.id = mp.media_id
		WHERE mp.media_id = ?
		ORDER BY
			CASE mp.occupation
				WHEN 'director' THEN 1 WHEN 'writer' THEN 2 WHEN 'actor' THEN 3 ELSE 4 END,
			CASE mp.role_type WHEN 'leading' THEN 1 WHEN 'supporting' THEN 2 ELSE 3 END,
			mp.sort_order ASC, p.name COLLATE NOCASE ASC
	`, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MediaPersonLink, 0)
	for rows.Next() {
		var l MediaPersonLink
		if rows.Scan(&l.ID, &l.MediaID, &l.PersonID, &l.PersonName, &l.AvatarURL,
			&l.Occupation, &l.CharacterName, &l.RoleType, &l.SortOrder,
			&l.MediaTitle, &l.MediaYear, &l.PosterURL) != nil {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// ListPersonWorks returns filmography for a person, optionally filtered by occupation.
func ListPersonWorks(db *sql.DB, personID int64, occupation string) ([]MediaPersonLink, error) {
	args := []any{personID}
	where := `WHERE mp.person_id = ?`
	if occ := strings.TrimSpace(occupation); occ != "" && ValidOccupations[occ] {
		where += ` AND mp.occupation = ?`
		args = append(args, occ)
	}
	rows, err := db.Query(`
		SELECT mp.id, mp.media_id, mp.person_id, p.name, COALESCE(p.avatar_url,''),
			mp.occupation, COALESCE(mp.character_name,''), COALESCE(mp.role_type,''), mp.sort_order,`+mediaPersonMediaSelect+`
		FROM media_person mp
		JOIN cast_person p ON p.id = mp.person_id
		JOIN media m ON m.id = mp.media_id AND m.status = 'active'
		`+where+mediaPersonMediaOrder+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MediaPersonLink, 0)
	for rows.Next() {
		var l MediaPersonLink
		if rows.Scan(&l.ID, &l.MediaID, &l.PersonID, &l.PersonName, &l.AvatarURL,
			&l.Occupation, &l.CharacterName, &l.RoleType, &l.SortOrder,
			&l.MediaTitle, &l.MediaYear, &l.PosterURL) != nil {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// OccupationCounts returns work counts grouped by occupation.
func OccupationCounts(db *sql.DB, personID int64) (map[string]int64, error) {
	rows, err := db.Query(`
		SELECT mp.occupation, COUNT(DISTINCT mp.media_id)
		FROM media_person mp
		JOIN media m ON m.id = mp.media_id AND m.status = 'active'
		WHERE mp.person_id = ?
		GROUP BY mp.occupation
	`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var occ string
		var n int64
		if rows.Scan(&occ, &n) == nil {
			out[occ] = n
		}
	}
	return out, nil
}

// ListCollaborators returns top collaborators for a person.
func ListCollaborators(db *sql.DB, personID int64, limit int) ([]Collaborator, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(`
		SELECT cp.id, cp.name, COALESCE(cp.avatar_url,''), COUNT(DISTINCT mp2.media_id) AS collab_count
		FROM media_person mp1
		JOIN media_person mp2 ON mp2.media_id = mp1.media_id AND mp2.person_id != mp1.person_id
		JOIN cast_person cp ON cp.id = mp2.person_id AND cp.deleted_at IS NULL
		WHERE mp1.person_id = ?
		GROUP BY cp.id, cp.name, cp.avatar_url
		ORDER BY collab_count DESC, cp.name COLLATE NOCASE ASC
		LIMIT ?
	`, personID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Collaborator, 0)
	for rows.Next() {
		var c Collaborator
		if rows.Scan(&c.PersonID, &c.Name, &c.AvatarURL, &c.CollaborationCount) != nil {
			continue
		}
		c.RecentMovieTitles, _ = recentCollaborationTitles(db, personID, c.PersonID, 3)
		out = append(out, c)
	}
	return out, nil
}

func recentCollaborationTitles(db *sql.DB, personA, personB int64, limit int) ([]string, error) {
	rows, err := db.Query(`
		SELECT DISTINCT m.title
		FROM media_person mp1
		JOIN media_person mp2 ON mp2.media_id = mp1.media_id AND mp2.person_id = ?
		JOIN media m ON m.id = mp1.media_id AND m.status = 'active'
		WHERE mp1.person_id = ?
		ORDER BY COALESCE(m.year,0) DESC
		LIMIT ?
	`, personB, personA, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var title string
		if rows.Scan(&title) == nil && strings.TrimSpace(title) != "" {
			out = append(out, title)
		}
	}
	return out, nil
}

// DeleteMediaLinks removes all cast links for a media item.
func DeleteMediaLinks(db *sql.DB, mediaID int64) {
	if db == nil || mediaID <= 0 {
		return
	}
	_, _ = db.Exec(`DELETE FROM media_person WHERE media_id = ?`, mediaID)
}

// CreatePerson inserts a new person record.
func CreatePerson(db *sql.DB, patch PersonPatch) (int64, error) {
	name := strings.TrimSpace(patch.Name)
	if name == "" {
		return 0, sql.ErrNoRows
	}
	gender := 0
	if patch.Gender != nil {
		gender = *patch.Gender
	}
	res, err := db.Exec(`
		INSERT INTO cast_person (
			name, name_norm, english_name, gender, birth_date, birth_place, nationality,
			occupation_json, biography, avatar_url, aliases, tmdb_id, imdb_id, douban_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, name, normName(name), strings.TrimSpace(patch.EnglishName), gender,
		strings.TrimSpace(patch.BirthDate), strings.TrimSpace(patch.BirthPlace), strings.TrimSpace(patch.Nationality),
		encodeOccupations(patch.Occupations), strings.TrimSpace(patch.Biography), strings.TrimSpace(patch.AvatarURL),
		strings.TrimSpace(patch.Aliases), strings.TrimSpace(patch.TMDBID), strings.TrimSpace(patch.IMDBID), strings.TrimSpace(patch.DoubanID))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SearchPersons finds persons matching a query (for global search).
func SearchPersons(db *sql.DB, query string, limit int) ([]Person, error) {
	query = strings.TrimSpace(query)
	if query == "" || db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	like := "%" + query + "%"
	rows, err := db.Query(`
		SELECT p.id, p.name, p.name_norm, COALESCE(p.english_name,''), COALESCE(p.gender,0),
			COALESCE(p.birth_date,''), COALESCE(p.birth_place,''), COALESCE(p.nationality,''),
			COALESCE(p.occupation_json,'[]'), COALESCE(p.biography,''), COALESCE(p.avatar_url,''),
			COALESCE(p.aliases,''), COALESCE(p.scraped,0), p.scraped_at,
			COALESCE(p.tmdb_id,''), COALESCE(p.imdb_id,''), COALESCE(p.douban_id,''),
			COALESCE(p.field_locks_json,'{}'), p.created_at, p.updated_at, p.deleted_at,
			(SELECT COUNT(DISTINCT media_id) FROM media_person WHERE person_id = p.id) AS work_count
		FROM cast_person p
		WHERE p.deleted_at IS NULL
		  AND (p.name LIKE ? OR p.english_name LIKE ? OR p.aliases LIKE ?)
		ORDER BY
			CASE WHEN p.name = ? THEN 0 WHEN p.name LIKE ? THEN 1 ELSE 2 END,
			p.name COLLATE NOCASE ASC
		LIMIT ?
	`, like, like, like, query, query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Person, 0)
	for rows.Next() {
		var p Person
		var occRaw, locksRaw, scrapedAt, deletedAt sql.NullString
		var scraped int64
		if err := rows.Scan(
			&p.ID, &p.Name, &p.NameNorm, &p.EnglishName, &p.Gender,
			&p.BirthDate, &p.BirthPlace, &p.Nationality, &occRaw,
			&p.Biography, &p.AvatarURL, &p.Aliases, &scraped, &scrapedAt,
			&p.TMDBID, &p.IMDBID, &p.DoubanID, &locksRaw,
			&p.CreatedAt, &p.UpdatedAt, &deletedAt, &p.WorkCount,
		); err != nil {
			continue
		}
		p.Occupations = decodeOccupations(occRaw.String)
		p.Scraped = scraped > 0
		out = append(out, p)
	}
	return out, nil
}

// MarkScraped sets scraped timestamp.
func MarkScraped(db *sql.DB, personID int64) {
	_, _ = db.Exec(`UPDATE cast_person SET scraped = 1, scraped_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		time.Now().UTC().Format("2006-01-02 15:04:05"), personID)
}

func findOrCreateByTMDBExecutor(ctx context.Context, db store.SQLExecutor, tmdbID, name string) (int64, error) {
	tmdbID = strings.TrimSpace(tmdbID)
	if tmdbID == "" {
		return findOrCreateByNameExecutor(ctx, db, name)
	}
	var id int64
	if db.QueryRowContext(ctx, `SELECT id FROM cast_person WHERE tmdb_id=? AND deleted_at IS NULL LIMIT 1`, tmdbID).Scan(&id) == nil && id > 0 {
		return id, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unknown"
	}
	r, e := db.ExecContext(ctx, `INSERT INTO cast_person(name,name_norm,tmdb_id,occupation_json) VALUES(?,?,?,'[]')`, name, normName(name), tmdbID)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func findOrCreateByNameExecutor(ctx context.Context, db store.SQLExecutor, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, sql.ErrNoRows
	}
	var id int64
	if db.QueryRowContext(ctx, `SELECT id FROM cast_person WHERE name_norm=? AND deleted_at IS NULL LIMIT 1`, normName(name)).Scan(&id) == nil && id > 0 {
		return id, nil
	}
	r, e := db.ExecContext(ctx, `INSERT INTO cast_person(name,name_norm,occupation_json) VALUES(?,?,'[]')`, name, normName(name))
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func linkMediaPersonExecutor(ctx context.Context, db store.SQLExecutor, mediaID, personID int64, occupation, character, role string, sortOrder int) error {
	occupation = strings.TrimSpace(occupation)
	if occupation == "" {
		occupation = OccActor
	}
	if !ValidOccupations[occupation] {
		occupation = OccOther
	}
	if sortOrder <= 0 {
		sortOrder = 9999
	}
	_, e := db.ExecContext(ctx, `INSERT INTO media_person(media_id,person_id,occupation,character_name,role_type,sort_order) VALUES(?,?,?,?,?,?) ON CONFLICT(media_id,person_id,occupation) DO UPDATE SET character_name=excluded.character_name,role_type=excluded.role_type,sort_order=excluded.sort_order,updated_at=CURRENT_TIMESTAMP`, mediaID, personID, occupation, strings.TrimSpace(character), strings.TrimSpace(role), sortOrder)
	return e
}
func mergePersonOccupationsExecutor(ctx context.Context, db store.SQLExecutor, id int64, occupations ...string) {
	var raw sql.NullString
	if db.QueryRowContext(ctx, `SELECT occupation_json FROM cast_person WHERE id=?`, id).Scan(&raw) != nil {
		return
	}
	existing := decodeOccupations(raw.String)
	seen := map[string]bool{}
	for _, o := range existing {
		seen[o] = true
	}
	for _, o := range occupations {
		o = strings.TrimSpace(o)
		if o != "" && ValidOccupations[o] && !seen[o] {
			seen[o] = true
			existing = append(existing, o)
		}
	}
	_, _ = db.ExecContext(ctx, `UPDATE cast_person SET occupation_json=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, encodeOccupations(existing), id)
}
func applyScrapePatchExecutor(ctx context.Context, db store.SQLExecutor, id int64, patch PersonPatch) error {
	var locksRaw, name sql.NullString
	if e := db.QueryRowContext(ctx, `SELECT name,field_locks_json FROM cast_person WHERE id=? AND deleted_at IS NULL`, id).Scan(&name, &locksRaw); e != nil {
		return e
	}
	locks := decodeFieldLocks(locksRaw.String)
	updates := []string{"updated_at=CURRENT_TIMESTAMP", "scraped=1", "scraped_at=CURRENT_TIMESTAMP"}
	args := []any{}
	if v := strings.TrimSpace(patch.Name); v != "" && !locks["name"] {
		updates = append(updates, "name=?", "name_norm=?")
		args = append(args, v, normName(v))
	}
	if v := strings.TrimSpace(patch.AvatarURL); v != "" && !locks["avatar_url"] {
		updates = append(updates, "avatar_url=?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(patch.TMDBID); v != "" && !locks["tmdb_id"] {
		updates = append(updates, "tmdb_id=?")
		args = append(args, v)
	}
	if len(patch.Occupations) > 0 && !locks["occupation"] {
		updates = append(updates, "occupation_json=?")
		args = append(args, encodeOccupations(patch.Occupations))
	}
	updates = append(updates, "field_locks_json=?")
	args = append(args, encodeFieldLocks(locks), id)
	_, e := db.ExecContext(ctx, "UPDATE cast_person SET "+strings.Join(updates, ",")+" WHERE id=?", args...)
	return e
}
