package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPostIngestDefaultsAndExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       PostIngestConfig
	}{
		{"defaults", "server: {}\n", PostIngestConfig{MaxConcurrent: defaultPostIngestGlobal(), PosterMaxConcurrent: 2, PreviewMaxConcurrent: 1}},
		{"explicit", "post_ingest:\n  max_concurrent: 3\n  poster_max_concurrent: 2\n  preview_max_concurrent: 2\n", PostIngestConfig{MaxConcurrent: 3, PosterMaxConcurrent: 2, PreviewMaxConcurrent: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if c.PostIngest.MaxConcurrent != tc.want.MaxConcurrent || c.PostIngest.PosterMaxConcurrent != tc.want.PosterMaxConcurrent || c.PostIngest.PreviewMaxConcurrent != tc.want.PreviewMaxConcurrent {
				t.Fatalf("PostIngest=%+v want %+v", c.PostIngest, tc.want)
			}
		})
	}
}

func TestPostIngestConfigValidate(t *testing.T) {
	valid := PostIngestConfig{MaxConcurrent: 4, PosterMaxConcurrent: 2, PreviewMaxConcurrent: 1}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		field string
		value PostIngestConfig
	}{
		{"MaxConcurrent", PostIngestConfig{MaxConcurrent: 0, PosterMaxConcurrent: 1, PreviewMaxConcurrent: 1}}, {"MaxConcurrent", PostIngestConfig{MaxConcurrent: 33, PosterMaxConcurrent: 1, PreviewMaxConcurrent: 1}},
		{"PosterMaxConcurrent", PostIngestConfig{MaxConcurrent: 4, PosterMaxConcurrent: 0, PreviewMaxConcurrent: 1}}, {"PosterMaxConcurrent", PostIngestConfig{MaxConcurrent: 4, PosterMaxConcurrent: 3, PreviewMaxConcurrent: 1}}, {"PosterMaxConcurrent", PostIngestConfig{MaxConcurrent: 1, PosterMaxConcurrent: 2, PreviewMaxConcurrent: 1}},
		{"PreviewMaxConcurrent", PostIngestConfig{MaxConcurrent: 4, PosterMaxConcurrent: 1, PreviewMaxConcurrent: 0}}, {"PreviewMaxConcurrent", PostIngestConfig{MaxConcurrent: 4, PosterMaxConcurrent: 1, PreviewMaxConcurrent: 3}}, {"PreviewMaxConcurrent", PostIngestConfig{MaxConcurrent: 1, PosterMaxConcurrent: 1, PreviewMaxConcurrent: 2}},
	}
	for _, tc := range cases {
		if err := tc.value.Validate(); err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Fatalf("%+v err=%v want %s", tc.value, err, tc.field)
		}
	}
}

func TestLoadPostIngestRejectsExplicitZero(t *testing.T) {
	cases := []struct{ key, field string }{
		{"max_concurrent", "MaxConcurrent"}, {"poster_max_concurrent", "PosterMaxConcurrent"}, {"preview_max_concurrent", "PreviewMaxConcurrent"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			yamlText := "post_ingest:\n  max_concurrent: 4\n  poster_max_concurrent: 2\n  preview_max_concurrent: 1\n"
			yamlText = strings.Replace(yamlText, tc.key+": "+map[string]string{"max_concurrent": "4", "poster_max_concurrent": "2", "preview_max_concurrent": "1"}[tc.key], tc.key+": 0", 1)
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(yamlText), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Load error=%v want %s", err, tc.field)
			}
		})
	}
}

func TestLoadPostIngestDefaultsOnlyMissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("post_ingest:\n  max_concurrent: 3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := PostIngestConfig{MaxConcurrent: 3, PosterMaxConcurrent: 2, PreviewMaxConcurrent: 1}
	if c.PostIngest.MaxConcurrent != want.MaxConcurrent || c.PostIngest.PosterMaxConcurrent != want.PosterMaxConcurrent || c.PostIngest.PreviewMaxConcurrent != want.PreviewMaxConcurrent {
		t.Fatalf("PostIngest=%+v", c.PostIngest)
	}
}

func TestDefaultPostIngestGlobalForCPU(t *testing.T) {
	cases := map[int]int{1: 2, 2: 2, 4: 2, 6: 3, 8: 4, 32: 4}
	for cpu, want := range cases {
		if got := defaultPostIngestGlobalForCPU(cpu); got != want {
			t.Errorf("cpu=%d got=%d want=%d", cpu, got, want)
		}
	}
}

func TestShippedConfigsUseCPUPostIngestDefaults(t *testing.T) {
	for _, path := range []string{"default/config.yml", filepath.Join("..", "..", "config.yml")} {
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
		if cfg.PostIngest.maxConcurrentSet || cfg.PostIngest.posterMaxConcurrentSet || cfg.PostIngest.previewMaxConcurrentSet {
			t.Fatalf("%s pins post_ingest: %+v", path, cfg.PostIngest)
		}
		if cfg.PostIngest.MaxConcurrent != defaultPostIngestGlobal() {
			t.Fatalf("%s max=%d", path, cfg.PostIngest.MaxConcurrent)
		}
	}
}

func TestShippedConfigsParseAsYAML(t *testing.T) {
	for _, path := range []string{"default/config.yml", filepath.Join("..", "..", "config.yml")} {
		if _, err := Load(path); err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
	}
}
