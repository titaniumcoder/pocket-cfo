package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/paidinvoices"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/users"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
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
	problems += validateFile(filepath.Join(dataDir, "accounts.json"), func(b []byte) error {
		var af accountsdata.AccountsFile
		return json.Unmarshal(b, &af)
	})
	problems += validatePaidInvoices(dataDir)
	problems += validateActuals(dataDir)

	if problems > 0 {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %d problem(s) found\n", problems)
		return 1
	}
	fmt.Println("pocket-cfo-ctl validate: OK")
	return 0
}

// validateActuals checks every data/actuals/*.json against its schema and
// budget.json's category ids. Like validatePaidInvoices it can't go through
// validateFile, which sees one file at a time.
func validateActuals(dataDir string) int {
	dir := filepath.Join(dataDir, "actuals")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: read %s: %v\n", dir, err)
		return 1
	}

	knownIDs := budgetCategoryIDs(dataDir)

	problems := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: read %s: %v\n", path, err)
			problems++
			continue
		}
		var af actualsdata.ActualsFile
		if err := json.Unmarshal(b, &af); err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %s: %v\n", path, err)
			problems++
			continue
		}
		if err := actualsdata.ValidateActuals(af, actualsdata.MonthKeyOf(e.Name()), knownIDs); err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %s: %v\n", path, err)
			problems++
		}
	}
	return problems
}

// validatePaidInvoices checks paid-invoices.json structurally and then against
// the invoices it references. The cross-file rules need the whole invoice set,
// so this can't go through validateFile's one-file-at-a-time check.
func validatePaidInvoices(dataDir string) int {
	path := filepath.Join(dataDir, "paid-invoices.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0 // nothing paid yet — see stats.LoadPaid
		}
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: read %s: %v\n", path, err)
		return 1
	}

	var pf paidinvoices.PaidInvoicesJson
	if err := json.Unmarshal(b, &pf); err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %s: %v\n", path, err)
		return 1
	}

	// A missing invoices dir reads as "no invoices", so each payment fails
	// with its own "no such invoice" rather than one opaque read error.
	invoices, err := stats.LoadInvoices(filepath.Join(dataDir, "invoices"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %s: %v\n", path, err)
		return 1
	}

	if err := stats.ValidatePaid(pf, invoices); err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl validate: %s: %v\n", path, err)
		return 1
	}
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
