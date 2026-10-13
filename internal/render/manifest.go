package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// ManifestEntry pins one artifact in build/. The two kinds pin different
// things, because the two kinds of artifact fail differently:
//
//   - A draft PDF is re-rendered on every run, so its entry records the HTML
//     it was rendered from — the same thing IsCurrent re-derives.
//   - An issued PDF is write-once and must survive template changes, so its
//     entry records the PDF's own bytes — an immutability record.
type ManifestEntry struct {
	HTMLHash string `json:"html,omitempty"`
	PDFHash  string `json:"pdf,omitempty"`
}

type Manifest map[string]ManifestEntry

// LoadManifest reads a manifest file. Entries written by pre-4.4.1 versions
// are plain hash strings pinning the rendered HTML; they load as HTMLHash and
// are migrated to PDFHash by the render command's skip path.
func LoadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Manifest{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	m := make(Manifest, len(raw))
	for name, v := range raw {
		var entry ManifestEntry
		if err := json.Unmarshal(v, &entry); err == nil && (entry.HTMLHash != "" || entry.PDFHash != "") {
			m[name] = entry
			continue
		}
		var legacy string
		if err := json.Unmarshal(v, &legacy); err != nil {
			return nil, fmt.Errorf("parse %s: entry %q: %w", path, name, err)
		}
		m[name] = ManifestEntry{HTMLHash: legacy}
	}
	return m, nil
}

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

func HashHTML(html []byte) string {
	return hashBytes(html)
}

func HashPDF(pdf []byte) string {
	return hashBytes(pdf)
}

// HashPDFFile hashes a PDF on disk — what IsCurrent compares an issued
// entry against, and what pins a stored PDF into the manifest without
// re-rendering it.
func HashPDFFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return HashPDF(b), nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
