package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/users"
)

// runValidate checks every data file PocketCFO reads at runtime — recipients,
// invoices, users.json, budget.json — against its schema-generated type
// (structural + required-field + enum validation, the same checks every
// runtime loader already applies implicitly via json.Unmarshal) plus
// budget.json's extra business-rule validation (ValidateBudget) that JSON
// Schema can't express. Meant to run in CI against a real data checkout
// (see the private pocket-cfo-data repo) before it's ever deployed — see
// PocketCFO's plan §3.2.
func runValidate(args []string) int {
	dataDir := "data"
	if len(args) > 0 {
		dataDir = args[0]
	}

	problems := 0
	problems += validateDir(filepath.Join(dataDir, "recipients"), func(b []byte) error {
		var r recipient.RecipientJson
		return json.Unmarshal(b, &r)
	})
	problems += validateDir(filepath.Join(dataDir, "invoices"), func(b []byte) error {
		var inv invoice.InvoiceJson
		return json.Unmarshal(b, &inv)
	})
	problems += validateFile(filepath.Join(dataDir, "users.json"), func(b []byte) error {
		var u users.UsersJson
		return json.Unmarshal(b, &u)
	})
	problems += validateFile(filepath.Join(dataDir, "budget.json"), func(b []byte) error {
		var bf budgetdata.BudgetFile
		if err := json.Unmarshal(b, &bf); err != nil {
			return err
		}
		return budgetdata.ValidateBudget(bf)
	})

	if problems > 0 {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %d problem(s) found\n", problems)
		return 1
	}
	fmt.Println("pocket-cfo-ctl validate: OK")
	return 0
}

// validateDir runs check against every *.json file directly under dir. A
// missing directory is fine (0 problems) — not every data checkout has
// every category of data (e.g. a fresh recipient list with no invoices yet).
func validateDir(dir string, check func([]byte) error) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: read %s: %v\n", dir, err)
		return 1
	}
	problems := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		problems += validateFile(filepath.Join(dir, e.Name()), check)
	}
	return problems
}

// validateFile runs check against path's contents. A missing file is fine
// (0 problems) — same reasoning as validateDir.
func validateFile(path string, check func([]byte) error) int {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: read %s: %v\n", path, err)
		return 1
	}
	if err := check(b); err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %s: %v\n", path, err)
		return 1
	}
	return 0
}
