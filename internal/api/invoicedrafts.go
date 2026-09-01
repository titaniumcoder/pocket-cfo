package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/notes"
	"github.com/titaniumcoder/pocket-cfo/internal/validate"
	"github.com/titaniumcoder/pocket-cfo/schemas"
)

const DefaultCatalogPath = "catalog/notes.json"

const draftStatus = "draft"
const issuedStatus = "issued"

type SaveDraftRequest struct {
	Document json.RawMessage `json:"document"`
	BaseSHA  string          `json:"base_sha,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

type DraftSaveResult struct {
	Number        string `json:"number"`
	Created       bool   `json:"created"`
	SHA           string `json:"sha"`
	DeployPending bool   `json:"deploy_pending"`
}

type IssueInvoiceRequest struct {
	Invoice string `json:"invoice"`
	Reason  string `json:"reason"`
}

type IssueInvoiceResult struct {
	Number        string `json:"number"`
	SHA           string `json:"sha,omitempty"`
	DeployPending bool   `json:"deploy_pending"`
}

type InvoiceDocument struct {
	Number   string          `json:"number"`
	Status   string          `json:"status"`
	SHA      string          `json:"sha,omitempty"`
	Document json.RawMessage `json:"document"`
}

func (s *Service) SaveDraftInvoice(ctx context.Context, req SaveDraftRequest) (*DraftSaveResult, error) {
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}
	if len(bytes.TrimSpace(req.Document)) == 0 {
		return nil, errorf(CodeInvalidRequest, "document is required — the complete invoice JSON, exactly as get_invoice_document returned it, edited")
	}

	draft, err := withInvoiceStatus(req.Document, draftStatus)
	if err != nil {
		return nil, errorf(CodeInvalidRequest, "%v", err)
	}
	sentNumber, err := invoiceNumberField(draft)
	if err != nil {
		return nil, errorf(CodeInvalidRequest, "%v", err)
	}

	if sentNumber == "" {
		if req.BaseSHA != "" {
			return nil, errorf(CodeInvalidRequest, "base_sha names a file that does not exist yet — a new invoice is created, not based on anything")
		}
		return s.createDraft(ctx, draft, req)
	}
	if !invoiceNumberRE.MatchString(sentNumber) {
		return nil, errorf(CodeInvalidRequest, "number %q must look like INV-0000000001 — a new invoice gets its number by leaving it out", sentNumber)
	}
	path := s.invoicePath(sentNumber)

	existing, sha, err := s.loadInvoiceRaw(ctx, sentNumber)
	if err != nil {
		if e, ok := err.(*Error); ok && e.Code == CodeNotFound {
			return nil, errorf(CodeNotFound, "no invoice %s exists — to create one, leave the number out and the next one is assigned", sentNumber)
		}
		return nil, err
	}
	existingDoc, err := parseInvoice(existing)
	if err != nil {
		return nil, err
	}
	if existingDoc.Status != draftStatus {
		return nil, errorf(CodeInvalidRequest, "%s is %s, and an issued invoice is never edited again — a correction is a new invoice, not an edit", sentNumber, existingDoc.Status)
	}
	if err := s.checkBaseSHA(req.BaseSHA, sha); err != nil {
		return nil, err
	}

	if sameInvoiceDocument(draft, existing) {
		return &DraftSaveResult{Number: sentNumber, SHA: sha}, nil
	}

	if err := s.validateInvoiceDocument(draft, sentNumber); err != nil {
		return nil, err
	}
	newSHA, err := s.Store.Put(ctx, path, draft, sha, invoiceCommitMessage(sentNumber, "draft updated", req.Reason))
	if err != nil {
		return nil, storeError(err, path)
	}
	return &DraftSaveResult{Number: sentNumber, SHA: newSHA, DeployPending: true}, nil
}

func (s *Service) createDraft(ctx context.Context, draft []byte, req SaveDraftRequest) (*DraftSaveResult, error) {
	number, err := s.nextInvoiceNumber(ctx)
	if err != nil {
		return nil, err
	}
	draft, err = withInvoiceNumber(draft, number)
	if err != nil {
		return nil, errorf(CodeInternal, "%v", err)
	}
	if err := s.validateInvoiceDocument(draft, number); err != nil {
		return nil, err
	}
	newSHA, err := s.Store.Put(ctx, s.invoicePath(number), draft, "", invoiceCommitMessage(number, "draft created", req.Reason))
	if err != nil {
		return nil, storeError(err, s.invoicePath(number))
	}
	return &DraftSaveResult{Number: number, Created: true, SHA: newSHA, DeployPending: true}, nil
}

func (s *Service) IssueInvoice(ctx context.Context, req IssueInvoiceRequest) (*IssueInvoiceResult, error) {
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}
	number := strings.TrimSpace(req.Invoice)
	if !invoiceNumberRE.MatchString(number) {
		return nil, errorf(CodeInvalidRequest, "invoice %q must look like INV-0000000001", req.Invoice)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, errorf(CodeInvalidRequest, "reason is required — issuing freezes this document forever, so say why it is ready")
	}

	raw, sha, err := s.loadInvoiceRaw(ctx, number)
	if err != nil {
		return nil, err
	}
	existing, err := parseInvoice(raw)
	if err != nil {
		return nil, err
	}
	if existing.Status == issuedStatus {
		return &IssueInvoiceResult{Number: number, SHA: sha}, nil
	}

	issued, err := withInvoiceStatus(raw, issuedStatus)
	if err != nil {
		return nil, errorf(CodeInternal, "%v", err)
	}
	if err := s.validateInvoiceDocument(issued, number); err != nil {
		return nil, err
	}
	newSHA, err := s.Store.Put(ctx, s.invoicePath(number), issued, sha, invoiceCommitMessage(number, "issued", req.Reason))
	if err != nil {
		return nil, storeError(err, s.invoicePath(number))
	}
	return &IssueInvoiceResult{Number: number, SHA: newSHA, DeployPending: true}, nil
}

func (s *Service) InvoiceDocumentFor(ctx context.Context, number string) (*InvoiceDocument, error) {
	if !invoiceNumberRE.MatchString(number) {
		return nil, errorf(CodeInvalidRequest, "invoice %q must look like INV-0000000001", number)
	}
	raw, sha, err := s.loadInvoiceRaw(ctx, number)
	if err != nil {
		return nil, err
	}
	doc, err := parseInvoice(raw)
	if err != nil {
		return nil, err
	}
	return &InvoiceDocument{
		Number:   doc.Number,
		Status:   string(doc.Status),
		SHA:      sha,
		Document: json.RawMessage(raw),
	}, nil
}

type invoicePDFTarget struct {
	Filename string
}

func (s *Service) InvoicePDFTarget(ctx context.Context, number, variant string) (*invoicePDFTarget, error) {
	if !invoiceNumberRE.MatchString(number) {
		return nil, errorf(CodeInvalidRequest, "invoice %q must look like INV-0000000001", number)
	}
	switch variant {
	case "draft":
		doc, err := s.InvoiceDocumentFor(ctx, number)
		if err != nil {
			return nil, err
		}
		if doc.Status != draftStatus {
			return nil, errorf(CodeInvalidRequest, "%s is not a draft anymore — its draft PDF went with the draft; download the original instead", number)
		}
		return &invoicePDFTarget{Filename: number + "-DRAFT.pdf"}, nil
	case "original":
		doc, err := s.InvoiceDocumentFor(ctx, number)
		if err != nil {
			return nil, err
		}
		if doc.Status == draftStatus {
			return nil, errorf(CodeInvalidRequest, "%s is a draft — only the draft variant exists for it; issue it first for an original", number)
		}
		return &invoicePDFTarget{Filename: number + ".pdf"}, nil
	case "paid":
		paid, err := s.paidMap(ctx)
		if err != nil {
			return nil, err
		}
		if _, ok := paid[number]; !ok {
			return nil, errorf(CodeInvalidRequest, "no payment is recorded for %s, so there is no paid PDF — set_invoice_paid is what creates one", number)
		}
		return &invoicePDFTarget{Filename: number + "-paid.pdf"}, nil
	default:
		return nil, errorf(CodeInvalidRequest, "variant %q is not one of draft, original or paid", variant)
	}
}

func (s *Service) loadInvoiceRaw(ctx context.Context, number string) ([]byte, string, error) {
	path := s.invoicePath(number)
	if s.Store != nil {
		raw, sha, err := s.Store.Get(ctx, path)
		if errors.Is(err, ErrNotFound) {
			return nil, "", errorf(CodeNotFound, "no invoice %s — list_invoices is the source of numbers", number)
		}
		if e, ok := err.(*Error); ok {
			return nil, "", e
		}
		if err != nil {
			return nil, "", errorf(CodeUpstream, "reading %s: %v", path, err)
		}
		return raw, sha, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", errorf(CodeNotFound, "no invoice %s — list_invoices is the source of numbers", number)
	}
	if err != nil {
		return nil, "", errorf(CodeInternal, "reading %s: %v", path, err)
	}
	return raw, "", nil
}

func (s *Service) nextInvoiceNumber(ctx context.Context) (string, error) {
	names, err := s.Store.List(ctx, s.invoicesPrefix())
	if err != nil {
		if e, ok := err.(*Error); ok {
			return "", e
		}
		return "", errorf(CodeUpstream, "listing %s: %v", s.invoicesPrefix(), err)
	}
	max := 0
	for _, name := range names {
		base := strings.TrimSuffix(name, ".json")
		if !invoiceNumberRE.MatchString(base) {
			continue
		}
		ordinal, err := strconv.Atoi(base[len("INV-"):])
		if err != nil {
			continue
		}
		if ordinal > max {
			max = ordinal
		}
	}
	return fmt.Sprintf("INV-%010d", max+1), nil
}

func (s *Service) checkBaseSHA(sent, current string) error {
	if sent == "" || sent == current {
		return nil
	}
	return &Error{
		Code:    CodeConflict,
		Message: "the invoice changed underneath this edit — re-read it with get_invoice_document and try again",
		Details: map[string]string{"base_sha": current},
	}
}

func (s *Service) validateInvoiceDocument(raw []byte, number string) error {
	if err := schemas.Validate(schemas.Invoice, raw); err != nil {
		return errorf(CodeValidationFailed, "the document does not satisfy invoice.json's schema: %v", err)
	}
	doc, err := parseInvoice(raw)
	if err != nil {
		return err
	}
	if doc.Number != number {
		return errorf(CodeValidationFailed, "the document names %s but it is being written to %s — number and file are the same thing", doc.Number, number)
	}
	cat, err := s.catalog()
	if err != nil {
		return err
	}
	vdoc := validate.Doc{Path: s.invoicePath(number), Base: number, Inv: doc}
	if err := validate.Invoice(vdoc, cat); err != nil {
		return errorf(CodeValidationFailed, "the document fails validation: %v", err)
	}
	return nil
}

func (s *Service) catalog() (*notes.NotesJson, error) {
	b, err := os.ReadFile(s.catalogPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errorf(CodeInternal, "no note catalog at %s — the mandatory wording on an invoice cannot be checked without it, so nothing is committed", s.catalogPath())
	}
	if err != nil {
		return nil, errorf(CodeInternal, "reading %s: %v", s.catalogPath(), err)
	}
	if err := schemas.Validate(schemas.Notes, b); err != nil {
		return nil, errorf(CodeInternal, "%s does not satisfy its schema: %v", s.catalogPath(), err)
	}
	var cat notes.NotesJson
	if err := json.Unmarshal(b, &cat); err != nil {
		return nil, errorf(CodeInternal, "%s does not parse: %v", s.catalogPath(), err)
	}
	return &cat, nil
}

func (s *Service) catalogPath() string {
	if s.CatalogPath != "" {
		return s.CatalogPath
	}
	return DefaultCatalogPath
}

func (s *Service) invoicePath(number string) string {
	return strings.TrimSuffix(s.invoicesPrefix(), "/") + "/" + number + ".json"
}

func (s *Service) invoicesPrefix() string {
	if s.InvoicesPrefix != "" {
		return s.InvoicesPrefix
	}
	return DefaultInvoicesDir
}

func parseInvoice(raw []byte) (*invoice.InvoiceJson, error) {
	var inv invoice.InvoiceJson
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, errorf(CodeValidationFailed, "the document does not parse as an invoice: %v", err)
	}
	return &inv, nil
}

// invoiceNumberField reads just the number member, without the generated
// unmarshaller's pattern checks — a document created without a number parses
// fine and is assigned one below.
func invoiceNumberField(raw []byte) (string, error) {
	var probe struct {
		Number string `json:"number"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", err
	}
	return strings.TrimSpace(probe.Number), nil
}

func withInvoiceNumber(raw []byte, number string) ([]byte, error) {
	members, tail, err := splitInvoiceMembers(raw)
	if err != nil {
		return nil, err
	}
	for i, m := range members {
		if m.key == "number" {
			members[i].value = []byte(strconv.Quote(number))
			return rebuildInvoiceMembers(members, tail), nil
		}
	}
	first := invoiceMember{
		key:     "number",
		keyText: []byte("\n  "),
		sep:     []byte(": "),
		value:   []byte(strconv.Quote(number)),
	}
	return rebuildInvoiceMembers(append([]invoiceMember{first}, members...), tail), nil
}

func sameInvoiceDocument(a, b []byte) bool {
	var x, y invoice.InvoiceJson
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ab, err := json.Marshal(x)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(y)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

func storeError(err error, path string) error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return errorf(CodeUpstream, "writing %s: %v", path, err)
}

func invoiceCommitMessage(number, verb, reason string) string {
	body := strings.TrimSpace(reason)
	if body != "" {
		body = "\n\n" + body
	}
	return fmt.Sprintf("feat(invoices): %s %s%s\n", number, verb, body)
}

type invoiceMember struct {
	key     string
	keyText []byte
	sep     []byte
	value   []byte
}

// splitInvoiceMembers walks the top level of a JSON object once, keeping every
// member's exact bytes. The committed diff of a status flip is then the status
// line alone, however the file is formatted.
func splitInvoiceMembers(src []byte) ([]invoiceMember, []byte, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, errors.New("the document is not a JSON object")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, errors.New("the document is not a JSON object")
	}

	var members []invoiceMember
	pos := dec.InputOffset()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, nil, errors.New("expected an object key")
		}
		keyEnd := dec.InputOffset()

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, nil, err
		}
		end := dec.InputOffset()

		sepStart := keyEnd
		i := keyEnd
		for i < end && isSpace(src[i]) {
			i++
		}
		if i >= end || src[i] != ':' {
			return nil, nil, errors.New("expected a colon after an object key")
		}
		i++
		for i < end && isSpace(src[i]) {
			i++
		}

		members = append(members, invoiceMember{
			key:     key,
			keyText: src[pos:keyEnd],
			sep:     src[sepStart:i],
			value:   src[i:end],
		})
		pos = end
		for i := end; i < int64(len(src)); i++ {
			switch src[i] {
			case ' ', '\t', '\r', '\n':
				continue
			case ',':
				pos = i + 1
			}
			break
		}
	}
	return members, src[pos:], nil
}

func rebuildInvoiceMembers(members []invoiceMember, tail []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(m.keyText)
		buf.Write(m.sep)
		buf.Write(m.value)
	}
	buf.Write(tail)
	return buf.Bytes()
}

func withInvoiceStatus(raw []byte, status string) ([]byte, error) {
	members, tail, err := splitInvoiceMembers(raw)
	if err != nil {
		return nil, err
	}
	found := false
	for i, m := range members {
		if m.key == "status" {
			members[i].value = []byte(`"` + status + `"`)
			found = true
		}
	}
	if !found {
		return nil, errors.New(`the document carries no "status" field, which invoice.json requires`)
	}
	return rebuildInvoiceMembers(members, tail), nil
}
