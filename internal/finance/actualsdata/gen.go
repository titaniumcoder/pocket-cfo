package actualsdata

//go:generate go run github.com/atombender/go-jsonschema@v0.23.1 -p actualsdata --schema-root-type "https://github.com/titaniumcoder/pocket-cfo/internal/finance/data/actuals.schema.json=ActualsFile" -o generated.go ../data/actuals.schema.json
