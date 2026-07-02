package photoclass

// SceneTags are AI content categories (Chinese labels per product spec).
var SceneTags = func() []string {
	out := make([]string, 0, 8)
	for _, c := range CategoryCatalog {
		if c.Kind == "scene" {
			out = append(out, c.Name)
		}
	}
	return out
}()

// ColorTags are derived from histogram analysis.
var ColorTags = func() []string {
	out := make([]string, 0, 4)
	for _, c := range CategoryCatalog {
		if c.Kind == "color" {
			out = append(out, c.Name)
		}
	}
	return out
}()

// SourceTags are inferred from metadata / filename.
var SourceTags = func() []string {
	out := make([]string, 0, 3)
	for _, c := range CategoryCatalog {
		if c.Kind == "source" {
			out = append(out, c.Name)
		}
	}
	return out
}()

// AllBuiltinTags returns every built-in tag for UI listing.
func AllBuiltinTags() []string {
	out := make([]string, 0, len(SceneTags)+len(ColorTags)+len(SourceTags))
	out = append(out, SceneTags...)
	out = append(out, ColorTags...)
	out = append(out, SourceTags...)
	return out
}
