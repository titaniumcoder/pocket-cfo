package accountsdata

//go:generate go run github.com/atombender/go-jsonschema@v0.23.1 -p accountsdata --schema-root-type "https://github.com/titaniumcoder/pocket-cfo/internal/finance/data/accounts.schema.json=AccountsFile" -o generated.go ../data/accounts.schema.json
