package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
)

// computeToken derives a recipient's client-portal token from their number,
// their own passkey, and the app-wide secret — fully stateless, nothing is
// stored. A recipient with no passkey set has no token to compute at all,
// which is the activation mechanism: setting access_passkey by hand in
// their JSON file is what turns the portal on for them.
func computeToken(recipientNumber int, passkey, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d:%s", recipientNumber, passkey)
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}

// findRecipientByToken is the only way to resolve a token back to a
// recipient — there's no way to invert an HMAC, so every recipient that has
// a passkey set gets its expected token recomputed and compared in constant
// time. Fine at this recipient volume (ARCHITECTURE.md: ~30 invoices/year).
func findRecipientByToken(token string, recipients []recipient.RecipientJson, secret string) (recipient.RecipientJson, bool) {
	for _, r := range recipients {
		if r.AccessPasskey == nil || *r.AccessPasskey == "" {
			continue
		}
		expected := computeToken(r.Number, *r.AccessPasskey, secret)
		if hmac.Equal([]byte(expected), []byte(token)) {
			return r, true
		}
	}
	return recipient.RecipientJson{}, false
}

// portalLinks builds the dashboard's copyable portal URL for every
// recipient that has a passkey set — nothing to show for recipients
// without one, since they have no working endpoint at all.
func (s *server) portalLinks(recipients []recipient.RecipientJson) map[int]string {
	links := map[int]string{}
	if s.cfg.clientLinkSecret == "" {
		return links
	}
	for _, r := range recipients {
		if r.AccessPasskey == nil || *r.AccessPasskey == "" {
			continue
		}
		token := computeToken(r.Number, *r.AccessPasskey, s.cfg.clientLinkSecret)
		links[r.Number] = s.cfg.baseURL + "/invoicing/client/" + token
	}
	return links
}

// clientInvoiceRow is one row of a client's own invoice list — only issued
// invoices are ever built into this, never drafts.
type clientInvoiceRow struct {
	Number     string
	Title      string
	IssueDate  string
	DueDate    string
	Paid       bool
	GrandTotal int64
	PDFPath    string
}

// handleClientPortal serves a recipient's own read-only invoice list, no
// login required — the token itself is the credential. 404 on any failure
// (no matching recipient) rather than a permission error, so a guess
// doesn't confirm anything about which part was wrong.
func (s *server) handleClientPortal(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	recipients, err := stats.LoadRecipients(recipientsDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found, ok := findRecipientByToken(token, recipients, s.cfg.clientLinkSecret)
	if !ok {
		http.NotFound(w, r)
		return
	}

	invoices, err := stats.LoadInvoices(invoicesDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var rows []clientInvoiceRow
	for _, inv := range invoices {
		if inv.Status != invoice.InvoiceJsonStatusIssued || inv.Recipient.Number != found.Number {
			continue
		}
		totals, err := money.Compute(inv)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pdfPath := inv.Number + ".pdf"
		if inv.Paid != nil {
			pdfPath = inv.Number + "-paid.pdf"
		}
		rows = append(rows, clientInvoiceRow{
			Number:     inv.Number,
			Title:      inv.Title,
			IssueDate:  render.FormatDate(inv.IssueDate),
			DueDate:    render.FormatDate(inv.DueDate),
			Paid:       inv.Paid != nil,
			GrandTotal: totals.GrandTotal,
			PDFPath:    "/invoicing/client/" + token + "/invoices/" + pdfPath,
		})
	}

	view := struct {
		LegalName string
		Invoices  []clientInvoiceRow
	}{
		LegalName: found.LegalName,
		Invoices:  rows,
	}

	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.clientTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleClientInvoicePDF serves build/{file} only if the token resolves to
// a recipient, the requested invoice actually belongs to that recipient,
// and it's issued — never a draft, regardless of what the URL asks for.
func (s *server) handleClientInvoicePDF(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	file := r.PathValue("file")

	recipients, err := stats.LoadRecipients(recipientsDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found, ok := findRecipientByToken(token, recipients, s.cfg.clientLinkSecret)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if !strings.HasSuffix(file, ".pdf") || strings.Contains(file, "/") || strings.Contains(file, "..") || strings.Contains(file, "DRAFT") {
		http.NotFound(w, r)
		return
	}
	number := strings.TrimSuffix(strings.TrimSuffix(file, "-paid.pdf"), ".pdf")

	invoices, err := stats.LoadInvoices(invoicesDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var match *invoice.InvoiceJson
	for _, inv := range invoices {
		if inv.Number == number {
			match = inv
			break
		}
	}
	if match == nil || match.Status != invoice.InvoiceJsonStatusIssued || match.Recipient.Number != found.Number {
		http.NotFound(w, r)
		return
	}

	path := buildDir + "/" + file
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	http.ServeFile(w, r, path)
}

func (s *server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\nDisallow: /invoicing/client/\n")
}
