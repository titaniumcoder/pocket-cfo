package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest_MissingFileIsEmpty(t *testing.T) {
	m, err := LoadManifest(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("got %v, want an empty manifest for a missing file", m)
	}
}

func TestManifest_SaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	want := Manifest{
		"INV-0000000001-DRAFT.pdf": ManifestEntry{HTMLHash: "abc123"},
		"INV-0000000001.pdf":       ManifestEntry{PDFHash: "def456"},
	}

	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %v, want %v", k, got[k], v)
		}
	}
}

func TestLoadManifest_LegacyStringEntriesAreHTMLHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	legacy := `{"INV-0000000001.pdf": "oldhtmlhash"}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if entry := got["INV-0000000001.pdf"]; entry.HTMLHash != "oldhtmlhash" || entry.PDFHash != "" {
		t.Errorf("legacy entry loaded as %+v, want HTMLHash-only", entry)
	}
}

func TestManifest_SaveWritesTypedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	m := Manifest{"INV-0000000001.pdf": ManifestEntry{PDFHash: "def456"}}
	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var raw map[string]json.RawMessage
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var entry struct {
		HTML string `json:"html"`
		PDF  string `json:"pdf"`
	}
	if err := json.Unmarshal(raw["INV-0000000001.pdf"], &entry); err != nil {
		t.Fatalf("entry is not an object: %v", err)
	}
	if entry.PDF != "def456" || entry.HTML != "" {
		t.Errorf("entry = %+v, want a pdf-only object", entry)
	}
}

func TestHashes_AreDeterministic(t *testing.T) {
	if a, b := HashHTML([]byte("<html>hello</html>")), HashHTML([]byte("<html>hello</html>")); a != b {
		t.Errorf("HashHTML is not deterministic: %q != %q", a, b)
	}
	if a, b := HashPDF([]byte("%PDF-hello")), HashPDF([]byte("%PDF-hello")); a != b {
		t.Errorf("HashPDF is not deterministic: %q != %q", a, b)
	}
	if c := HashPDF([]byte("%PDF-different")); c == HashPDF([]byte("%PDF-hello")) {
		t.Error("HashPDF produced the same hash for different content")
	}
}

func TestHashPDFFile_HashesWhatIsOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "INV-0000000001.pdf")
	if err := os.WriteFile(path, []byte("%PDF-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashPDFFile(path)
	if err != nil {
		t.Fatalf("HashPDFFile: %v", err)
	}
	if want := HashPDF([]byte("%PDF-bytes")); got != want {
		t.Errorf("HashPDFFile = %q, want %q", got, want)
	}
	if _, err := HashPDFFile(filepath.Join(dir, "missing.pdf")); err == nil {
		t.Error("HashPDFFile succeeded for a missing file")
	}
}
