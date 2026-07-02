package doccover

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"knox-media/internal/store"
)

func TestPath(t *testing.T) {
	got := Path("/data/preview", 42)
	want := filepath.Join("/data/preview", "documents", "42", "cover.jpg")
	if got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestCoverFreshUsesSourceMtime(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.pdf")
	cover := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(src, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcMtime := time.Unix(1700000000, 0).UTC()
	if err := os.Chtimes(src, srcMtime, srcMtime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cover, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	coverMtime := srcMtime
	if err := os.Chtimes(cover, coverMtime, coverMtime); err != nil {
		t.Fatal(err)
	}
	if !coverFresh(cover, srcMtime.Unix()) {
		t.Fatal("expected fresh cover when mtime matches source")
	}
	newer := srcMtime.Add(time.Hour)
	if err := os.Chtimes(src, newer, newer); err != nil {
		t.Fatal(err)
	}
	if coverFresh(cover, newer.Unix()) {
		t.Fatal("expected stale cover after source mtime advanced")
	}
}

func TestExtractEPUBCoverFindsNamedCover(t *testing.T) {
	dir := t.TempDir()
	epub := filepath.Join(dir, "sample.epub")
	cache := filepath.Join(dir, "cover.jpg")
	payload := []byte("fake-jpeg")
	if err := writeMiniEPUB(epub, "OEBPS/images/cover.jpg", payload); err != nil {
		t.Fatal(err)
	}
	got := ExtractEPUBCover(epub, cache)
	if got != cache {
		t.Fatalf("ExtractEPUBCover() = %q, want %q", got, cache)
	}
	data, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("cover bytes = %q, want %q", data, payload)
	}
}

func writeMiniEPUB(path, coverName string, coverData []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create(coverName)
	if err != nil {
		return err
	}
	if _, err := w.Write(coverData); err != nil {
		return err
	}
	return zw.Close()
}

func TestCachedCoverPlaintext(t *testing.T) {
	dir := t.TempDir()
	preview := filepath.Join(dir, "preview")
	id := int64(42)
	cover := Path(preview, id)
	if err := os.MkdirAll(filepath.Dir(cover), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cover, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !CachedCover(nil, preview, dir, id, 0) {
		t.Fatal("expected plaintext cover")
	}
}

func TestCachedCoverEncryptedDerived(t *testing.T) {
	dir := t.TempDir()
	preview := filepath.Join(dir, "preview")
	derivedBase := filepath.Join(dir, "derived")
	id := int64(99)
	enc := filepath.Join(derivedBase, "99", "doc_cover", "cover.jpg.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc-jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'docs', 'document', ?)`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (?, 1, 'f1', 'doc', 'x.pdf', 'document', 'active')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO media_derived_assets (media_id, artifact_kind, logical_name, enc_path, wrapped_dek, iv)
		VALUES (?, ?, ?, ?, 'aa', 'bb')`, id, docCoverKind, docCoverLogicalName, enc); err != nil {
		t.Fatal(err)
	}
	if Exists(preview, id) {
		t.Fatal("plaintext should be absent")
	}
	if !CachedCover(db, preview, derivedBase, id, 0) {
		t.Fatal("expected encrypted derived cover")
	}
}

func TestCoverStrategy(t *testing.T) {
	cases := []struct {
		path string
		want coverStrategy
	}{
		{"book.epub", strategyEPUB},
		{"book.pdf", strategyPDF},
		{"report.docx", strategyOffice},
		{"sheet.xls", strategyOffice},
		{"photo.jpg", strategyImage},
		{"notes.txt", strategyNone},
		{"catalog.enc", strategyNone},
		{filepath.Join(t.TempDir(), "knox-plain-1.pdf"), strategyPDF},
	}
	for _, tc := range cases {
		if got := coverStrategyFor(tc.path); got != tc.want {
			t.Fatalf("coverStrategyFor(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
