// Package buildinfo answers the two questions a deploy otherwise leaves open:
// which build is this, and which data is it looking at.
//
// The two arrive by different routes on purpose. The version is fixed when the
// binary is compiled; the data is bind-mounted long afterwards by whoever runs
// the container, so only they can say which commit it is.
package buildinfo

import (
	"strings"
	"time"
)

// Version is stamped with -X at build time — see the Dockerfile and the
// Makefile. "dev" is what a plain `go build` produces, which is every local
// build that does not go through one of those.
var Version = "dev"

// Data describes the mounted data checkout, read from the environment rather
// than compiled in. The zero value means nobody supplied it, and nothing is
// rendered for it.
var Data DataStamp

type DataStamp struct {
	UpdatedAt string // as given, ideally YYYY-MM-DD
	Commit    string
}

func (d DataStamp) Empty() bool {
	return strings.TrimSpace(d.UpdatedAt) == "" && strings.TrimSpace(d.Commit) == ""
}

// String renders the stamp for display: "17.08.2026 - a1b2c3d". Either half on
// its own still renders; both missing renders nothing.
func (d DataStamp) String() string {
	parts := make([]string, 0, 2)
	if day := formatDay(strings.TrimSpace(d.UpdatedAt)); day != "" {
		parts = append(parts, day)
	}
	if sha := shortSHA(strings.TrimSpace(d.Commit)); sha != "" {
		parts = append(parts, sha)
	}
	return strings.Join(parts, " - ")
}

// formatDay matches the date format the rest of the UI uses (render.FormatDate).
//
// A value it cannot parse is passed through as given rather than dropped: a
// misconfigured deploy should look wrong on the page, not identical to one
// that was never configured at all.
func formatDay(raw string) string {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("02.01.2006")
		}
	}
	return raw
}

const shortSHALength = 7

func shortSHA(raw string) string {
	if len(raw) > shortSHALength {
		return raw[:shortSHALength]
	}
	return raw
}
