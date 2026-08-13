package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

type Manifest map[string]string

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
	sum := sha256.Sum256(html)
	return hex.EncodeToString(sum[:])
}
