package render

import (
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
	want := Manifest{"INV-0000000001.pdf": "abc123", "INV-0000000001-paid.pdf": "def456"}

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
			t.Errorf("got[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestHashHTML_Deterministic(t *testing.T) {
	a := HashHTML([]byte("<html>hello</html>"))
	b := HashHTML([]byte("<html>hello</html>"))
	if a != b {
		t.Errorf("HashHTML is not deterministic: %q != %q", a, b)
	}
	if c := HashHTML([]byte("<html>different</html>")); c == a {
		t.Error("HashHTML produced the same hash for different content")
	}
}
