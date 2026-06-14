package handler

import "strings"

const mediaSearchLikePerToken = 17

func splitMediaSearchTokens(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return []string{query}
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return []string{query}
	}
	return out
}

func mediaSearchOrClause() string {
	return `
		m.title LIKE ? ESCAPE '\'
		OR m.original_title LIKE ? ESCAPE '\'
		OR m.file_path LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.scrape.overview'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.scrape.genres'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.scrape.tagline'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.scrape.extra.series_overview'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.scrape.extra.series_title'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.document.author'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.document.text_preview'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.document.description'), '') LIKE ? ESCAPE '\'
		OR COALESCE(json_extract(m.meta_json, '$.photo.tags'), '') LIKE ? ESCAPE '\'
		OR m.meta_json LIKE ? ESCAPE '\'
		OR EXISTS (
			SELECT 1 FROM music_track mt
			JOIN music_album a ON a.id = mt.album_id
			LEFT JOIN music_artist ar ON ar.id = a.album_artist_id
			WHERE mt.media_id = m.id AND (
				COALESCE(a.title, '') LIKE ? ESCAPE '\'
				OR COALESCE(mt.artist_display, '') LIKE ? ESCAPE '\'
				OR COALESCE(ar.name, '') LIKE ? ESCAPE '\'
				OR COALESCE(mt.title, '') LIKE ? ESCAPE '\'
			)
		)`
}

func appendMediaTextSearchFilter(baseQuery string, args []any, query string) (string, []any) {
	tokens := splitMediaSearchTokens(query)
	if len(tokens) == 0 {
		return baseQuery, args
	}
	for _, token := range tokens {
		like := "%" + escapeLike(token) + "%"
		baseQuery += ` AND (` + mediaSearchOrClause() + `)`
		for i := 0; i < mediaSearchLikePerToken; i++ {
			args = append(args, like)
		}
	}
	return baseQuery, args
}
