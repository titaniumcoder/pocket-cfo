// Package data embeds the finance schemas (schema definitions ship embedded,
// same convention as invoicer's own schemas/ — see AGENTS.md). budget.json
// itself is data, not schema, so it does NOT live here: it's
// read fresh from disk at runtime (data/budget.json by default, overridable
// via DATA_DIR — see cmd/pocketcfo and internal/finance/tracker.Budget),
// same "hand-edited, read fresh, never embedded" convention as
// data/recipients, data/invoices, data/users.json.
package data

import "embed"

//go:embed budget.schema.json accounts.schema.json
var FS embed.FS
