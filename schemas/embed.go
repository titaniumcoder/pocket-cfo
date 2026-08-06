// Package schemas embeds the JSON Schema definitions that ship with the
// pocketcfo binary, so they're available at runtime without needing
// schemas/ present on disk.
package schemas

import "embed"

//go:embed *.json
var FS embed.FS
