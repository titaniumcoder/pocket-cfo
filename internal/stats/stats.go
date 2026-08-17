package stats

import (
	"encoding/json"
	"fmt"
	"log"
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
		invoices = append(invoices, &inv)
	}
	return invoices, nil
}

type RecipientRow struct {
	Number       int
	LegalName    string
	AddressLine  string
	Email        string
	TotalInFrame int64
	Outstanding  int64
}

type InvoiceRow struct {
	Number        string
	Title         string
	RecipientName string
	IssueDate     types.SerializableDate
	DueDate       types.SerializableDate
	Paid          *types.SerializableDate
	GrandTotal    int64
	State         string
}

func deriveState(inv *invoice.InvoiceJson, paidOn *types.SerializableDate, today time.Time) string {
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		return "draft"
	}
	if paidOn != nil {
		return "paid"
	}
	if dayOf(inv.DueDate.Time).Before(dayOf(today)) {
		return "overdue"
	}
	return "issued"
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func Aggregate(invoices []*invoice.InvoiceJson, recipients []recipient.RecipientJson, paid map[string]types.SerializableDate, year *int, now time.Time) (years []int, recipientRows []RecipientRow, invoiceRows []InvoiceRow, err error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	all, years, err := computeAll(invoices, paid, today)
	if err != nil {
		return nil, nil, nil, err
	}

	inFrame := func(c computedInvoice) bool {
		return year == nil || c.inv.IssueDate.Year() == *year
	}

	recipientRows = aggregateRecipientRows(recipients, all, inFrame)
	invoiceRows = aggregateInvoiceRows(all, inFrame)

	return years, recipientRows, invoiceRows, nil
}

type computedInvoice struct {
	inv    *invoice.InvoiceJson
	total  int64
	state  string
	paidOn *types.SerializableDate
}

func computeAll(invoices []*invoice.InvoiceJson, paid map[string]types.SerializableDate, today time.Time) (all []computedInvoice, years []int, err error) {
	all = make([]computedInvoice, 0, len(invoices))
	yearSet := map[int]bool{}
	for _, inv := range invoices {
		totals, err := money.Compute(inv)
		if err != nil {
			if inv.Status == invoice.InvoiceJsonStatusDraft {
				log.Printf("stats: skipping draft %s, which does not compute: %v", inv.Number, err)
				continue
			}
			return nil, nil, fmt.Errorf("compute totals for %s: %w", inv.Number, err)
		}
		var paidOn *types.SerializableDate
		if d, ok := paid[inv.Number]; ok {
			paidOn = &d
		}
		all = append(all, computedInvoice{
			inv: inv, total: totals.GrandTotal, state: deriveState(inv, paidOn, today), paidOn: paidOn,
		})
		yearSet[inv.IssueDate.Year()] = true
	}
	for y := range yearSet {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	return all, years, nil
}

func aggregateRecipientRows(recipients []recipient.RecipientJson, all []computedInvoice, inFrame func(computedInvoice) bool) []RecipientRow {
	byRecipient := map[int][]computedInvoice{}
	for _, c := range all {
		n := c.inv.Recipient.Number
		byRecipient[n] = append(byRecipient[n], c)
	}

	var rows []RecipientRow
	for _, r := range recipients {
		cs, ok := byRecipient[r.Number]
		if !ok {
			continue
		}

		hasActivity := false
		for _, c := range cs {
			if inFrame(c) {
				hasActivity = true
				break
			}
		}
		if !hasActivity {
			continue
		}

		row := RecipientRow{
			Number:      r.Number,
			LegalName:   r.LegalName,
			AddressLine: formatAddress(r.Address),
			Email:       r.Email,
		}
		for _, c := range cs {
			if c.inv.Status != invoice.InvoiceJsonStatusIssued {
				continue
			}
			if inFrame(c) {
				row.TotalInFrame += c.total
			}
			if c.paidOn == nil {
				row.Outstanding += c.total
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].LegalName < rows[j].LegalName
	})
	return rows
}

func aggregateInvoiceRows(all []computedInvoice, inFrame func(computedInvoice) bool) []InvoiceRow {
	var rows []InvoiceRow
	for _, c := range all {
		if !inFrame(c) {
			continue
		}
		rows = append(rows, InvoiceRow{
			Number:        c.inv.Number,
			Title:         c.inv.Title,
			RecipientName: c.inv.Recipient.LegalName,
			IssueDate:     c.inv.IssueDate,
			DueDate:       c.inv.DueDate,
			Paid:          c.paidOn,
			GrandTotal:    c.total,
			State:         c.state,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].IssueDate.Equal(rows[j].IssueDate.Time) {
			return rows[i].IssueDate.After(rows[j].IssueDate.Time)
		}
		return rows[i].Number > rows[j].Number
	})
	return rows
}

func formatAddress(a recipient.Address) string {
	line1 := a.Line1
	if a.Line2 != nil && *a.Line2 != "" {
		line1 += ", " + *a.Line2
	}
	return fmt.Sprintf("%s, %s %s, %s", line1, a.PostalCode, a.City, render.CountryName(a.CountryCode))
}
