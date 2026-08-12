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

func computeToken(recipientNumber int, passkey, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d:%s", recipientNumber, passkey)
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}

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

type clientInvoiceRow struct {
	Number     string
	Title      string
	IssueDate  string
	DueDate    string
	Paid       bool
	GrandTotal int64
	PDFPath    string
}

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

	paid, err := stats.LoadPaid(paidInvoicesPath)
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
		_, isPaid := paid[inv.Number]
		pdfPath := inv.Number + ".pdf"
		if isPaid {
			pdfPath = inv.Number + "-paid.pdf"
		}
		rows = append(rows, clientInvoiceRow{
			Number:     inv.Number,
			Title:      inv.Title,
			IssueDate:  render.FormatDate(inv.IssueDate),
			DueDate:    render.FormatDate(inv.DueDate),
			Paid:       isPaid,
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
