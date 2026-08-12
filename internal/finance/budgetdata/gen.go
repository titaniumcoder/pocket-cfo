package budgetdata

//go:generate go run github.com/atombender/go-jsonschema@v0.23.1 -p budgetdata --schema-root-type "https://github.com/titaniumcoder/pocket-cfo/internal/finance/data/budget.schema.json=BudgetFile" -o generated.go ../data/budget.schema.json
