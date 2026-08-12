package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

// DefaultBudgetPath is where the plan lives in the data repo.
const DefaultBudgetPath = "data/budget.json"

var dayRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// MoveRequest shifts one one-off to the month it was actually charged in.
type MoveRequest struct {
	CategoryID string `json:"category_id"`
	FromMonth  string `json:"from_month"`
	ToMonth    string `json:"to_month"`
	Reason     string `json:"reason"`
	BaseSHA    string `json:"base_sha"`
}

// MoveResult reports the commit.
type MoveResult struct {
	CategoryID    string `json:"category_id"`
	Name          string `json:"name"`
	From          string `json:"from"`
	To            string `json:"to"`
	SHA           string `json:"sha"`
	DeployPending bool   `json:"deploy_pending"`
}

// MovePlannedExpense changes one dated category's month and nothing else.
//
// Deliberately not general budget.json write access: the plan drives every
// figure on the dashboard, so a general endpoint there has a far larger blast
// radius than actuals. This cannot change an amount, add or remove a
// category, touch overrides, or move anything that isn't already a one-off —
// and it proves that about its own output before committing.
func (s *Service) MovePlannedExpense(ctx context.Context, req MoveRequest) (*MoveResult, error) {
	if req.CategoryID == "" {
		return nil, errorf(CodeInvalidRequest, "category_id is required")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, errorf(CodeInvalidRequest, "reason is required, and lands in the commit message")
	}
	if _, _, err := ParseMonth(req.FromMonth); err != nil {
		return nil, errorf(CodeInvalidRequest, "from_month %q must look like 2026-08", req.FromMonth)
	}
	if _, _, err := ParseMonth(req.ToMonth); err != nil {
		return nil, errorf(CodeInvalidRequest, "to_month %q must look like 2026-08", req.ToMonth)
	}
	if req.FromMonth == req.ToMonth {
		return nil, errorf(CodeInvalidRequest, "from_month and to_month are the same")
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}

	path := s.budgetPath()
	src, sha, err := s.Store.Get(ctx, path)
	if err != nil {
		if err == ErrNotFound {
			return nil, errorf(CodeNotFound, "%s does not exist", path)
		}
		if e, ok := err.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "reading %s: %v", path, err)
	}
	if req.BaseSHA != sha {
		return nil, &Error{
			Code:    CodeConflict,
			Message: fmt.Sprintf("%s has moved on; re-read it and retry", path),
			Details: map[string]string{"current_sha": sha},
		}
	}

	var before budgetdata.BudgetFile
	if err := json.Unmarshal(src, &before); err != nil {
		return nil, errorf(CodeUpstream, "%s does not parse: %v", path, err)
	}
	cat, found := findCategory(before, req.CategoryID)
	if !found {
		return nil, errorf(CodeInvalidRequest, "no category %q in %s", req.CategoryID, path)
	}
	if cat.Date == nil || *cat.Date == "" {
		return nil, errorf(CodeInvalidRequest, "%q recurs monthly; only a one-off has a month to move", cat.Name)
	}
	if got := monthOf(*cat.Date); got != req.FromMonth {
		return nil, &Error{
			Code:    CodeConflict,
			Message: fmt.Sprintf("%q is planned for %s, not %s — re-read the budget", cat.Name, got, req.FromMonth),
		}
	}

	// Keep the day, move the month: the day is informational, and preserving
	// it keeps the diff to the part that matters.
	newDate := req.ToMonth + (*cat.Date)[7:]
	out, err := setCategoryDate(src, req.CategoryID, newDate)
	if err != nil {
		return nil, err
	}
	if err := verifyOnlyDateChanged(src, out, req.CategoryID, newDate); err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("fix(budget): move %s from %s to %s\n\n%s\n", cat.Name, req.FromMonth, req.ToMonth, strings.TrimSpace(req.Reason))
	newSHA, perr := s.Store.Put(ctx, path, out, sha, msg)
	if perr != nil {
		if e, ok := perr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "writing %s: %v", path, perr)
	}

	return &MoveResult{
		CategoryID: req.CategoryID, Name: cat.Name,
		From: req.FromMonth, To: req.ToMonth,
		SHA: newSHA, DeployPending: true,
	}, nil
}

func (s *Service) budgetPath() string {
	if s.BudgetPath != "" {
		return s.BudgetPath
	}
	return DefaultBudgetPath
}

func findCategory(bf budgetdata.BudgetFile, id string) (budgetdata.Category, bool) {
	for _, g := range bf.Groups {
		for _, c := range g.Categories {
			if c.Id == id {
				return c, true
			}
		}
	}
	return budgetdata.Category{}, false
}

func monthOf(date string) string {
	if len(date) < 7 {
		return ""
	}
	return date[:7]
}

// setCategoryDate rewrites one category's date, leaving every other byte as it
// was. budget.json is hand-maintained; re-marshalling would reorder and
// reformat the whole file for a one-field change.
func setCategoryDate(src []byte, categoryID, newDate string) ([]byte, error) {
	if !dayRE.MatchString(newDate) {
		return nil, errorf(CodeInternal, "computed date %q is malformed", newDate)
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, errorf(CodeUpstream, "budget.json: %v", err)
	}

	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, errorf(CodeUpstream, "budget.json: %v", err)
		}
		if key != "groups" {
			if err := skipValue(dec); err != nil {
				return nil, errorf(CodeUpstream, "budget.json: %v", err)
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return nil, errorf(CodeUpstream, "budget.json: %v", err)
		}
		for dec.More() { // group
			if err := expectDelim(dec, '{'); err != nil {
				return nil, errorf(CodeUpstream, "budget.json: %v", err)
			}
			for dec.More() {
				gkey, err := objectKey(dec)
				if err != nil {
					return nil, errorf(CodeUpstream, "budget.json: %v", err)
				}
				if gkey != "categories" {
					if err := skipValue(dec); err != nil {
						return nil, errorf(CodeUpstream, "budget.json: %v", err)
					}
					continue
				}
				if err := expectDelim(dec, '['); err != nil {
					return nil, errorf(CodeUpstream, "budget.json: %v", err)
				}
				for dec.More() { // category
					out, done, err := rewriteCategory(dec, src, categoryID, newDate)
					if err != nil {
						return nil, err
					}
					if done {
						return out, nil
					}
				}
				if err := expectDelim(dec, ']'); err != nil {
					return nil, errorf(CodeUpstream, "budget.json: %v", err)
				}
			}
			if err := expectDelim(dec, '}'); err != nil {
				return nil, errorf(CodeUpstream, "budget.json: %v", err)
			}
		}
	}
	return nil, errorf(CodeInvalidRequest, "no category %q in budget.json", categoryID)
}

// rewriteCategory consumes one category object. When it is the target, it
// returns the whole file with that category's date replaced or inserted.
func rewriteCategory(dec *json.Decoder, src []byte, categoryID, newDate string) (out []byte, done bool, err error) {
	if derr := expectDelim(dec, '{'); derr != nil {
		return nil, false, errorf(CodeUpstream, "budget.json: %v", derr)
	}
	afterBrace := int(dec.InputOffset())

	id := ""
	dateStart, dateEnd := -1, -1
	for dec.More() {
		// InputOffset sits after the previous value, so step over the comma
		// and whitespace to the key's own opening quote — otherwise replacing
		// the pair would swallow the separator.
		before := keyStart(src, int(dec.InputOffset()))
		key, kerr := objectKey(dec)
		if kerr != nil {
			return nil, false, errorf(CodeUpstream, "budget.json: %v", kerr)
		}
		switch key {
		case "id":
			if id, err = stringValue(dec); err != nil {
				return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
			}
		case "date":
			if _, err = stringValue(dec); err != nil {
				return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
			}
			dateStart, dateEnd = before, int(dec.InputOffset())
		default:
			if serr := skipValue(dec); serr != nil {
				return nil, false, errorf(CodeUpstream, "budget.json: %v", serr)
			}
		}
	}
	if derr := expectDelim(dec, '}'); derr != nil {
		return nil, false, errorf(CodeUpstream, "budget.json: %v", derr)
	}
	if id != categoryID {
		return nil, false, nil
	}

	var buf bytes.Buffer
	if dateStart >= 0 {
		// Everything up to the key is kept verbatim, including the comma and
		// the indentation that led into it.
		buf.Write(src[:dateStart])
		buf.WriteString(`"date": "` + newDate + `"`)
		buf.Write(src[dateEnd:])
	} else {
		ins := leadingSpace(src[afterBrace:]) + `"date": "` + newDate + `",`
		buf.Write(src[:afterBrace])
		buf.WriteString(ins)
		buf.Write(src[afterBrace:])
	}
	return buf.Bytes(), true, nil
}

// verifyOnlyDateChanged proves the edit did exactly one thing before anything
// is committed. Byte surgery on the file that drives every figure is only safe
// if it checks its own work.
func verifyOnlyDateChanged(before, after []byte, categoryID, newDate string) error {
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(after, &bf); err != nil {
		return errorf(CodeInternal, "the result does not satisfy budget.schema.json: %v", err)
	}
	if err := budgetdata.ValidateBudget(bf); err != nil {
		return errorf(CodeInternal, "the result fails validation: %v", err)
	}
	cat, found := findCategory(bf, categoryID)
	if !found || cat.Date == nil || *cat.Date != newDate {
		return errorf(CodeInternal, "the result does not carry the new date")
	}

	var a, b any
	if err := json.Unmarshal(before, &a); err != nil {
		return errorf(CodeUpstream, "budget.json does not parse: %v", err)
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return errorf(CodeInternal, "the result does not parse: %v", err)
	}
	stripCategoryDate(a, categoryID)
	stripCategoryDate(b, categoryID)
	if !reflect.DeepEqual(a, b) {
		return errorf(CodeInternal, "the result differs from the original by more than one category's date")
	}
	return nil
}

// stripCategoryDate removes the date from the one category being moved, so
// the rest of the document can be compared for equality.
func stripCategoryDate(doc any, categoryID string) {
	root, ok := doc.(map[string]any)
	if !ok {
		return
	}
	groups, ok := root["groups"].([]any)
	if !ok {
		return
	}
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		cats, ok := group["categories"].([]any)
		if !ok {
			continue
		}
		for _, c := range cats {
			cat, ok := c.(map[string]any)
			if ok && cat["id"] == categoryID {
				delete(cat, "date")
			}
		}
	}
}

// The JSON walking helpers, shared with the actuals write path.
func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

func objectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected an object key, got %v", tok)
	}
	return key, nil
}

func stringValue(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected a string, got %v", tok)
	}
	return s, nil
}

func skipValue(dec *json.Decoder) error {
	var raw json.RawMessage
	return dec.Decode(&raw)
}

// keyStart advances from a decoder offset to the opening quote of the next
// object key, stepping over the separating comma and any whitespace.
func keyStart(src []byte, from int) int {
	for i := from; i < len(src); i++ {
		if src[i] == '"' {
			return i
		}
	}
	return from
}

func leadingSpace(b []byte) string {
	for i, c := range b {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return string(b[:i])
		}
	}
	return ""
}
