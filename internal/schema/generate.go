package schema

//go:generate go tool go-jsonschema -p issuer    -o issuer/issuer.go       ../../schemas/issuer.json
//go:generate go tool go-jsonschema -p recipient -o recipient/recipient.go ../../schemas/recipient.json
//go:generate go tool go-jsonschema -p invoice   -o invoice/invoice.go     ../../schemas/invoice.json
//go:generate go tool go-jsonschema -p notes     -o notes/notes.go         ../../schemas/notes.json
//go:generate go tool go-jsonschema -p users     -o users/users.go        ../../schemas/users.json
//go:generate go tool go-jsonschema -p paidinvoices -o paidinvoices/paidinvoices.go ../../schemas/paid-invoices.json
