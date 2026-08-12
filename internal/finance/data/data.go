package data

import "embed"

//go:embed budget.schema.json accounts.schema.json actuals.schema.json
var FS embed.FS
