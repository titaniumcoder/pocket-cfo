package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Manifest records, per rendered PDF filename (e.g. "INV-0000000001.pdf",
// "INV-0000000004-DRAFT.pdf", "INV-0000000001-paid.pdf"), the hex SHA-256
// of the HTML that produced it — the precomputed reference value the
// staleness check (see staleness.go) compares against. invoicectl render
// writes this; the web app only ever reads it.
type Manifest map[string]string

// LoadManifest reads path. A missing file is an empty Manifest, not an
// error — true on the very first run after this feature lands, and
// whenever build/ hasn't been generated yet.
func LoadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Manifest{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// Save writes m to path as indented JSON.
func (m Manifest) Save(path string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// HashHTML returns the hex SHA-256 of html.
func HashHTML(html []byte) string {
	sum := sha256.Sum256(html)
	return hex.EncodeToString(sum[:])
}
