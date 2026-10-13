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

type invoicePDFTarget struct {
	Filename string
}

func (s *Service) InvoicePDFTarget(ctx context.Context, number, variant string) (*invoicePDFTarget, error) {
	if !invoiceNumberRE.MatchString(number) {
		return nil, errorf(CodeInvalidRequest, "invoice %q must look like INV-0000000001", number)
	}
	raw, _, err := s.loadInvoiceRaw(ctx, number)
	if err != nil {
		return nil, err
	}
	var doc invoice.InvoiceJson
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, errorf(CodeUpstream, "the committed %s does not parse: %v", number, err)
	}
	switch variant {
	case "draft":
		if doc.Status != invoice.InvoiceJsonStatusDraft {
			return nil, errorf(CodeInvalidRequest, "%s is not a draft anymore — its draft PDF went with the draft; download the original instead", number)
		}
		return &invoicePDFTarget{Filename: number + "-DRAFT.pdf"}, nil
	case "original":
		if doc.Status == invoice.InvoiceJsonStatusDraft {
			return nil, errorf(CodeInvalidRequest, "%s is a draft — only the draft variant exists for it; issue it first for an original", number)
		}
		return &invoicePDFTarget{Filename: number + ".pdf"}, nil
	case "paid":
		paid, err := s.paidMap(ctx)
		if err != nil {
			return nil, err
		}
		if _, ok := paid[number]; !ok {
			return nil, errorf(CodeInvalidRequest, "no payment is recorded for %s, so there is no paid PDF — payments are recorded in paid-invoices.json in the data repo", number)
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

// invoiceStatusField reads just the status member, without the generated
// unmarshaller's enum checks — a refusal can name what was sent even when it
// is not a legal value.
func invoiceStatusField(raw []byte) (string, error) {
	var probe struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", err
	}
	return strings.TrimSpace(probe.Status), nil
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
		keyText: []byte("\n  \"number\""),
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

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
