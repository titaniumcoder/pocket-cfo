// Package stats loads invoices/recipients from disk and aggregates them for
// the read-only web app. See ARCHITECTURE.md §3.8 for the derived-state
// definitions (outstanding, etc.) this reuses.
package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
)

// LoadRecipients reads every recipient JSON file under dir.
func LoadRecipients(dir string) ([]recipient.RecipientJson, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var recipients []recipient.RecipientJson
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var r recipient.RecipientJson
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		recipients = append(recipients, r)
	}
	return recipients, nil
}

// LoadInvoices reads every invoice JSON file under dir and returns every one
// except annulled invoices. Annulled invoices aren't live ledger items and
// stay excluded per ARCHITECTURE.md §3.8 ("annulled wins over everything").
// Drafts are included — the dashboard shows them, clearly marked, rather
// than hiding unfinished work — but see Aggregate, which keeps them out of
// the recipient ledger totals.
func LoadInvoices(dir string) ([]*invoice.InvoiceJson, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var invoices []*invoice.InvoiceJson
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var inv invoice.InvoiceJson
		if err := json.Unmarshal(b, &inv); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if inv.Annulment != nil {
			continue
		}
		invoices = append(invoices, &inv)
	}
	return invoices, nil
}

// RecipientRow is one row of the recipient summary table.
type RecipientRow struct {
	Number       int
	LegalName    string
	AddressLine  string
	Email        string
	TotalInFrame int64
	Outstanding  int64
}

// InvoiceRow is one row of the invoice list table.
type InvoiceRow struct {
	Number        string
	Title         string
	RecipientName string
	IssueDate     types.SerializableDate
	DueDate       types.SerializableDate
	Paid          *types.SerializableDate
	GrandTotal    int64
	State         string // one of "draft", "issued", "overdue", "paid" — see deriveState
}

// deriveState computes the single ledger/lifecycle state a row is marked
// with. Precedence — draft wins over paid/overdue/issued — mirrors
// ARCHITECTURE.md §3.8's "annulled wins over everything" pattern applied one
// level down: a draft is a lifecycle state, not a ledger state, and the
// template keys both its badge and its PDF link off this single value so
// the two can never disagree.
func deriveState(inv *invoice.InvoiceJson, today time.Time) string {
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		return "draft"
	}
	if inv.Paid != nil {
		return "paid"
	}
	if inv.DueDate.Time.Before(today) {
		return "overdue"
	}
	return "issued"
}

// Aggregate groups invoices (already loaded via LoadInvoices) by recipient
// and computes the totals the dashboard shows. year selects a single
// issue-date year to scope TotalInFrame and the invoice rows to; nil means
// "All". years is every distinct issue-date year present, for the nav,
// always unfiltered. Outstanding is always computed across every year,
// independent of the year filter — per ARCHITECTURE.md §3.8 and the user's
// explicit requirement. now is the request-time clock used to derive
// overdue/open state (ARCHITECTURE.md §3.8: computed at request time, never
// stored) — callers pass time.Now(), tests pass a fixed clock.
//
// A recipient appears in recipientRows when they have activity — any
// invoice, draft included — in the selected year (or ever, for "All"). A
// draft is activity even though it isn't money yet; only the ledger totals
// (TotalInFrame, Outstanding) stay restricted to issued invoices, since an
// unsent, mutable draft isn't a receivable.
func Aggregate(invoices []*invoice.InvoiceJson, recipients []recipient.RecipientJson, year *int, now time.Time) (years []int, recipientRows []RecipientRow, invoiceRows []InvoiceRow, err error) {
	today := now.UTC()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	type computed struct {
		inv   *invoice.InvoiceJson
		total int64
		state string
	}
	all := make([]computed, 0, len(invoices))
	yearSet := map[int]bool{}
	for _, inv := range invoices {
		totals, err := money.Compute(inv)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("compute totals for %s: %w", inv.Number, err)
		}
		all = append(all, computed{inv: inv, total: totals.GrandTotal, state: deriveState(inv, today)})
		yearSet[inv.IssueDate.Year()] = true
	}

	for y := range yearSet {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))

	inFrame := func(c computed) bool {
		return year == nil || c.inv.IssueDate.Year() == *year
	}

	// byRecipient holds every non-annulled invoice, draft included — a
	// recipient is shown when they have activity in the selected year, and a
	// draft is activity even though it isn't money yet. Ledger totals below
	// still only accumulate issued invoices; a draft is unsent and mutable,
	// not a receivable.
	byRecipient := map[int][]computed{}
	for _, c := range all {
		n := c.inv.Recipient.Number
		byRecipient[n] = append(byRecipient[n], c)
	}

	for _, r := range recipients {
		cs, ok := byRecipient[r.Number]
		if !ok {
			continue // no invoices at all, ever — not shown
		}

		hasActivity := false
		for _, c := range cs {
			if inFrame(c) {
				hasActivity = true
				break
			}
		}
		if !hasActivity {
			continue // no activity (draft or issued) in the selected year
		}

		row := RecipientRow{
			Number:      r.Number,
			LegalName:   r.LegalName,
			AddressLine: formatAddress(r.Address),
			Email:       r.Email,
		}
		for _, c := range cs {
			if c.inv.Status != invoice.InvoiceJsonStatusIssued {
				continue // drafts aren't a receivable — never in ledger totals
			}
			if inFrame(c) {
				row.TotalInFrame += c.total
			}
			if c.inv.Paid == nil {
				row.Outstanding += c.total
			}
		}
		recipientRows = append(recipientRows, row)
	}
	sort.Slice(recipientRows, func(i, j int) bool {
		return recipientRows[i].LegalName < recipientRows[j].LegalName
	})

	for _, c := range all {
		if !inFrame(c) {
			continue
		}
		invoiceRows = append(invoiceRows, InvoiceRow{
			Number:        c.inv.Number,
			Title:         c.inv.Title,
			RecipientName: c.inv.Recipient.LegalName,
			IssueDate:     c.inv.IssueDate,
			DueDate:       c.inv.DueDate,
			Paid:          c.inv.Paid,
			GrandTotal:    c.total,
			State:         c.state,
		})
	}
	sort.Slice(invoiceRows, func(i, j int) bool {
		if !invoiceRows[i].IssueDate.Equal(invoiceRows[j].IssueDate.Time) {
			return invoiceRows[i].IssueDate.After(invoiceRows[j].IssueDate.Time)
		}
		return invoiceRows[i].Number > invoiceRows[j].Number
	})

	return years, recipientRows, invoiceRows, nil
}

func formatAddress(a recipient.Address) string {
	line1 := a.Line1
	if a.Line2 != nil && *a.Line2 != "" {
		line1 += ", " + *a.Line2
	}
	return fmt.Sprintf("%s, %s %s, %s", line1, a.PostalCode, a.City, render.CountryName(a.CountryCode))
}
