// Package schema hosts go:generate directives that regenerate Go structs
// from schemas/*.json using go-jsonschema (a dev-time tool dependency, see
// go.mod and AGENTS.md). Each model gets its own subpackage below to avoid
// struct-name collisions (Address, etc. would collide if all four landed in
// one package). Run `go generate ./...` or `make generate` to regenerate;
// never hand-edit the generated files.
package schema

//go:generate go tool go-jsonschema -p issuer    -o issuer/issuer.go       ../../schemas/issuer.json
//go:generate go tool go-jsonschema -p recipient -o recipient/recipient.go ../../schemas/recipient.json
//go:generate go tool go-jsonschema -p invoice   -o invoice/invoice.go     ../../schemas/invoice.json
//go:generate go tool go-jsonschema -p notes     -o notes/notes.go         ../../schemas/notes.json
//go:generate go tool go-jsonschema -p users     -o users/users.go        ../../schemas/users.json
//go:generate go tool go-jsonschema -p paidinvoices -o paidinvoices/paidinvoices.go ../../schemas/paid-invoices.json
