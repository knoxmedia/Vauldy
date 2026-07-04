package caststore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"knox-media/internal/scraper"
)

// ImportCredits links TMDB credits to media and upserts person records.
func ImportCredits(db *sql.DB, mediaID int64, credits []scraper.CreditMember, avatarBaseURL string) (int, error) {
	if db == nil || mediaID <= 0 || len(credits) == 0 {
		return 0, nil
	}
	imported := 0
	for _, c := range credits {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		personID, err := FindOrCreateByTMDB(db, c.TMDBPersonID, name)
		if err != nil || personID <= 0 {
			continue
		}
		avatar := strings.TrimSpace(c.ProfilePath)
		if avatar != "" && !strings.HasPrefix(avatar, "http") {
			base := strings.TrimSpace(avatarBaseURL)
			if base == "" {
				base = "https://image.tmdb.org/t/p/w185"
			}
			if strings.HasPrefix(avatar, "/") {
				avatar = base + avatar
			} else {
				avatar = base + "/" + avatar
			}
		}
		patch := PersonPatch{
			Name:        name,
			AvatarURL:   avatar,
			TMDBID:      c.TMDBPersonID,
			Occupations: []string{c.Occupation},
		}
		_ = ApplyScrapePatch(db, personID, patch)
		MergePersonOccupations(db, personID, c.Occupation)
		if err := LinkMediaPerson(db, mediaID, personID, c.Occupation, c.CharacterName, c.RoleType, c.SortOrder); err != nil {
			continue
		}
		imported++
	}
	return imported, nil
}

// BackfillFromMetaJSON creates person links from legacy scrape.extra.cast JSON.
func BackfillFromMetaJSON(db *sql.DB, mediaID int64, metaJSON string) (int, error) {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" || db == nil || mediaID <= 0 {
		return 0, nil
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return 0, err
	}
	scrape, _ := meta["scrape"].(map[string]any)
	if scrape == nil {
		return 0, nil
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra == nil {
		return 0, nil
	}
	count := 0
	// Directors
	for _, key := range []string{"director", "directors"} {
		for _, name := range metaNameValues(extra[key]) {
			if name == "" {
				continue
			}
			pid, err := FindOrCreateByName(db, name)
			if err != nil || pid <= 0 {
				continue
			}
			MergePersonOccupations(db, pid, OccDirector)
			if LinkMediaPerson(db, mediaID, pid, OccDirector, "", "", 1) == nil {
				count++
			}
		}
	}
	// Producers
	for _, key := range []string{"producer", "producers"} {
		for _, name := range metaNameValues(extra[key]) {
			if name == "" {
				continue
			}
			pid, err := FindOrCreateByName(db, name)
			if err != nil || pid <= 0 {
				continue
			}
			MergePersonOccupations(db, pid, OccProducer)
			if LinkMediaPerson(db, mediaID, pid, OccProducer, "", "", 1) == nil {
				count++
			}
		}
	}
	// Cast
	castRaw, _ := extra["cast"].([]any)
	if castRaw == nil {
		castRaw, _ = extra["actors"].([]any)
	}
	for i, v := range castRaw {
		var name, character, tmdbID, avatar string
		switch row := v.(type) {
		case string:
			name = strings.TrimSpace(row)
		case map[string]any:
			name = strings.TrimSpace(fmt.Sprint(row["name"]))
			if name == "" {
				name = strings.TrimSpace(fmt.Sprint(row["actor"]))
			}
			character = strings.TrimSpace(fmt.Sprint(row["character"]))
			if character == "" {
				character = strings.TrimSpace(fmt.Sprint(row["role"]))
			}
			tmdbID = strings.TrimSpace(fmt.Sprint(row["id"]))
			if tmdbID == "0" || tmdbID == "<nil>" {
				tmdbID = ""
			}
			avatar = strings.TrimSpace(fmt.Sprint(row["profile_path"]))
			if avatar == "" {
				avatar = strings.TrimSpace(fmt.Sprint(row["avatar"]))
			}
		default:
			continue
		}
		if name == "" {
			continue
		}
		var pid int64
		var err error
		if tmdbID != "" {
			pid, err = FindOrCreateByTMDB(db, tmdbID, name)
		} else {
			pid, err = FindOrCreateByName(db, name)
		}
		if err != nil || pid <= 0 {
			continue
		}
		if avatar != "" && !strings.HasPrefix(avatar, "http") {
			avatar = "https://image.tmdb.org/t/p/w185" + avatar
		}
		if avatar != "" {
			_ = ApplyScrapePatch(db, pid, PersonPatch{AvatarURL: avatar, TMDBID: tmdbID})
		}
		MergePersonOccupations(db, pid, OccActor)
		roleType := RoleSupporting
		if i < 3 {
			roleType = RoleLeading
		}
		if LinkMediaPerson(db, mediaID, pid, OccActor, character, roleType, i) == nil {
			count++
		}
	}
	return count, nil
}

// PersonRef is a minimal person record for name lookup.
type PersonRef struct {
	ID        int64
	Name      string
	AvatarURL string
}

func appendMetaNames(out *[]string, seen map[string]bool, values ...any) {
	for _, v := range values {
		switch row := v.(type) {
		case string:
			name := strings.TrimSpace(row)
			if name == "" {
				continue
			}
			key := normName(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			*out = append(*out, name)
		case []any:
			for _, item := range row {
				appendMetaNames(out, seen, item)
			}
		case map[string]any:
			name := strings.TrimSpace(fmt.Sprint(row["name"]))
			if name == "" {
				name = strings.TrimSpace(fmt.Sprint(row["actor"]))
			}
			appendMetaNames(out, seen, name)
		}
	}
}

// CollectMetaPersonNames extracts cast/crew names from scrape metadata.
func CollectMetaPersonNames(metaJSON string) []string {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return nil
	}
	var meta map[string]any
	if json.Unmarshal([]byte(metaJSON), &meta) != nil {
		return nil
	}
	scrape, _ := meta["scrape"].(map[string]any)
	if scrape == nil {
		return nil
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	names := make([]string, 0)
	seen := map[string]bool{}
	for _, key := range []string{"director", "directors", "producer", "producers", "writer", "writers", "author", "authors"} {
		appendMetaNames(&names, seen, extra[key])
	}
	appendMetaNames(&names, seen, extra["cast"], extra["actors"])
	return names
}

// LookupPersonsByNames finds existing persons by name, english name, or aliases.
func LookupPersonsByNames(db *sql.DB, names []string) []PersonRef {
	if db == nil || len(names) == 0 {
		return nil
	}
	out := make([]PersonRef, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := normName(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		if ref, ok := lookupPersonRefByName(db, name); ok {
			out = append(out, ref)
		}
	}
	return out
}

func lookupPersonRefByName(db *sql.DB, name string) (PersonRef, bool) {
	name = strings.TrimSpace(name)
	if name == "" || db == nil {
		return PersonRef{}, false
	}
	key := normName(name)
	queries := []struct {
		q    string
		args []any
	}{
		{`SELECT id, name, COALESCE(avatar_url,'') FROM cast_person WHERE deleted_at IS NULL AND name_norm = ? LIMIT 1`, []any{key}},
		{`SELECT id, name, COALESCE(avatar_url,'') FROM cast_person WHERE deleted_at IS NULL AND trim(name) = ? LIMIT 1`, []any{name}},
		{`SELECT id, name, COALESCE(avatar_url,'') FROM cast_person WHERE deleted_at IS NULL AND trim(english_name) = ? LIMIT 1`, []any{name}},
	}
	for _, query := range queries {
		var id int64
		var dbName, avatar sql.NullString
		if db.QueryRow(query.q, query.args...).Scan(&id, &dbName, &avatar) == nil && id > 0 {
			return PersonRef{ID: id, Name: name, AvatarURL: avatar.String}, true
		}
	}
	rows, err := db.Query(`
		SELECT id, name, COALESCE(avatar_url,''), COALESCE(aliases,'')
		FROM cast_person
		WHERE deleted_at IS NULL AND aliases != ''
	`)
	if err != nil {
		return PersonRef{}, false
	}
	defer rows.Close()
	nameLower := strings.ToLower(name)
	for rows.Next() {
		var id int64
		var dbName, avatar, aliases sql.NullString
		if rows.Scan(&id, &dbName, &avatar, &aliases) != nil || id <= 0 {
			continue
		}
		for _, alias := range splitPersonAliases(aliases.String) {
			if strings.EqualFold(strings.TrimSpace(alias), name) || strings.ToLower(strings.TrimSpace(alias)) == nameLower {
				return PersonRef{ID: id, Name: name, AvatarURL: avatar.String}, true
			}
		}
	}
	return PersonRef{}, false
}

func splitPersonAliases(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, sep := range []string{",", ";", "，", "；", "|", "/"} {
		raw = strings.ReplaceAll(raw, sep, ",")
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func metaNameValues(v any) []string {
	switch row := v.(type) {
	case string:
		name := strings.TrimSpace(row)
		if name == "" {
			return nil
		}
		return []string{name}
	case []any:
		out := make([]string, 0, len(row))
		for _, item := range row {
			out = append(out, metaNameValues(item)...)
		}
		return out
	case map[string]any:
		name := strings.TrimSpace(fmt.Sprint(row["name"]))
		if name == "" {
			name = strings.TrimSpace(fmt.Sprint(row["actor"]))
		}
		if name == "" {
			return nil
		}
		return []string{name}
	default:
		return nil
	}
}

// HasMediaPersonLinks reports whether relational cast data exists.
func HasMediaPersonLinks(db *sql.DB, mediaID int64) bool {
	var n int64
	_ = db.QueryRow(`SELECT COUNT(1) FROM media_person WHERE media_id = ?`, mediaID).Scan(&n)
	return n > 0
}

// StatsSummary returns aggregate cast statistics.
func StatsSummary(db *sql.DB) (map[string]any, error) {
	out := map[string]any{}
	var total, scraped int64
	_ = db.QueryRow(`SELECT COUNT(1) FROM cast_person WHERE deleted_at IS NULL`).Scan(&total)
	_ = db.QueryRow(`SELECT COUNT(1) FROM cast_person WHERE deleted_at IS NULL AND scraped = 1`).Scan(&scraped)
	out["total_persons"] = total
	out["scraped_persons"] = scraped
	out["unscraped_persons"] = total - scraped
	rows, err := db.Query(`
		SELECT occupation, COUNT(DISTINCT person_id)
		FROM media_person
		GROUP BY occupation
	`)
	if err == nil {
		defer rows.Close()
		occ := map[string]int64{}
		for rows.Next() {
			var k string
			var v int64
			if rows.Scan(&k, &v) == nil {
				occ[k] = v
			}
		}
		out["occupation_distribution"] = occ
	}
	var avg float64
	_ = db.QueryRow(`
		SELECT AVG(cnt) FROM (
			SELECT COUNT(DISTINCT person_id) AS cnt FROM media_person GROUP BY media_id
		)
	`).Scan(&avg)
	out["avg_cast_per_media"] = avg
	return out, nil
}

// ApplyProfileFromScraper maps scraper profile to person patch.
func ApplyProfileFromScraper(db *sql.DB, personID int64, profile *scraper.PersonProfile, lockUserFields bool) error {
	if profile == nil {
		return sql.ErrNoRows
	}
	g := profile.Gender
	patch := PersonPatch{
		Name:        profile.Name,
		EnglishName: profile.EnglishName,
		Gender:      &g,
		BirthDate:   profile.BirthDate,
		BirthPlace:  profile.BirthPlace,
		Biography:   profile.Biography,
		AvatarURL:   profile.AvatarURL,
		Aliases:     profile.Aliases,
		Occupations: profile.Occupations,
		TMDBID:      profile.TMDBID,
		IMDBID:      profile.IMDBID,
	}
	scraped := true
	patch.Scraped = &scraped
	if lockUserFields {
		return ApplyScrapePatch(db, personID, patch)
	}
	return ApplyScrapePatch(db, personID, patch)
}
