package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

type ScheduleAmountChangeRequest struct {
	CategoryID    string   `json:"category_id"`
	FromMonth     string   `json:"from_month"`
	Amount        *float64 `json:"amount,omitempty"`
	MinimalAmount *float64 `json:"minimal_amount,omitempty"`
	Remove        bool     `json:"remove,omitempty"`
	Reason        string   `json:"reason"`
	BaseSHA       string   `json:"base_sha"`
}

type ScheduleAmountChangeResult struct {
	CategoryID    string   `json:"category_id"`
	Name          string   `json:"name"`
	From          string   `json:"from"`
	Amount        *float64 `json:"amount,omitempty"`
	MinimalAmount *float64 `json:"minimal_amount,omitempty"`
	Removed       bool     `json:"removed,omitempty"`
	SHA           string   `json:"sha"`
	DeployPending bool     `json:"deploy_pending"`
}

func (s *Service) ScheduleAmountChange(ctx context.Context, req ScheduleAmountChangeRequest) (*ScheduleAmountChangeResult, error) {
	if req.CategoryID == "" {
		return nil, errorf(CodeInvalidRequest, "category_id is required")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, errorf(CodeInvalidRequest, "reason is required, and lands in the commit message")
	}
	year, month, err := ParseMonth(req.FromMonth)
	if err != nil {
		return nil, errorf(CodeInvalidRequest, "from_month %q must look like 2026-08", req.FromMonth)
	}
	if req.Remove == (req.Amount != nil) {
		if req.Remove {
			return nil, errorf(CodeInvalidRequest, "send either an amount or remove, not both")
		}
		return nil, errorf(CodeInvalidRequest, "send amount (and optionally minimal_amount), or set remove to undo a scheduled change")
	}
	if req.Amount != nil && *req.Amount < 0 {
		return nil, errorf(CodeInvalidRequest, "amount is %s — a price is never negative", formatAmount(*req.Amount))
	}
	if req.MinimalAmount != nil {
		if *req.MinimalAmount < 0 {
			return nil, errorf(CodeInvalidRequest, "minimal_amount is %s — a price is never negative", formatAmount(*req.MinimalAmount))
		}
		if req.Amount != nil && *req.MinimalAmount > *req.Amount {
			return nil, errorf(CodeInvalidRequest,
				"minimal_amount %s is above the amount %s it is meant to reduce", formatAmount(*req.MinimalAmount), formatAmount(*req.Amount))
		}
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}

	now := s.now()
	if year < now.Year() || (year == now.Year() && month <= now.Month()) {
		return nil, errorf(CodeInvalidRequest,
			"cannot plan a schedule change on an already closed budget — %s is already in force, please fix it in budget.json", req.FromMonth)
	}

	path := s.budgetPath()
	src, sha, gerr := s.Store.Get(ctx, path)
	if gerr != nil {
		if gerr == ErrNotFound {
			return nil, errorf(CodeNotFound, "%s does not exist", path)
		}
		if e, ok := gerr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "reading %s: %v", path, gerr)
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
	if cat.Date != nil {
		return nil, errorf(CodeInvalidRequest, "%q is a one-off — a single price, full stop, so it has no months to change in", cat.Name)
	}
	if cat.From != nil && monthOf(*cat.From) > req.FromMonth {
		return nil, errorf(CodeInvalidRequest, "%q's change for %s starts before its own from %s, so it could never take effect", cat.Name, req.FromMonth, *cat.From)
	}
	if cat.Until != nil && req.FromMonth > monthOf(*cat.Until) {
		return nil, errorf(CodeInvalidRequest, "%q's change for %s starts after its own until %s, so it could never take effect", cat.Name, req.FromMonth, *cat.Until)
	}
	if req.Remove && !hasChangeAt(cat, req.FromMonth) {
		return nil, errorf(CodeNotFound, "%q has no scheduled change for %s to remove", cat.Name, req.FromMonth)
	}

	out, err := rewriteAmountChanges(src, req.CategoryID, req.FromMonth, req.Amount, req.MinimalAmount, req.Remove)
	if err != nil {
		return nil, err
	}
	if err := verifyOnlyAmountChangesChanged(src, out, req.CategoryID); err != nil {
		return nil, err
	}

	msg := scheduleChangeMessage(cat.Name, req.FromMonth, req, hasChangeAt(cat, req.FromMonth))
	newSHA, perr := s.Store.Put(ctx, path, out, sha, msg)
	if perr != nil {
		if e, ok := perr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "writing %s: %v", path, perr)
	}
	s.Budget.Publish(out)

	return &ScheduleAmountChangeResult{
		CategoryID: req.CategoryID, Name: cat.Name, From: req.FromMonth,
		Amount: req.Amount, MinimalAmount: req.MinimalAmount, Removed: req.Remove,
		SHA: newSHA, DeployPending: true,
	}, nil
}

func hasChangeAt(c budgetdata.Category, fromMonth string) bool {
	for _, ch := range c.AmountChanges {
		if monthOf(ch.From) == fromMonth {
			return true
		}
	}
	return false
}

func scheduleChangeMessage(name, fromMonth string, req ScheduleAmountChangeRequest, correcting bool) string {
	switch {
	case req.Remove:
		return fmt.Sprintf("fix(budget): remove %s's scheduled change for %s\n\n%s\n", name, fromMonth, strings.TrimSpace(req.Reason))
	case correcting:
		return fmt.Sprintf("fix(budget): correct %s's scheduled change for %s to %s\n\n%s\n", name, fromMonth, formatAmount(*req.Amount), strings.TrimSpace(req.Reason))
	default:
		return fmt.Sprintf("feat(budget): schedule %s to %s from %s\n\n%s\n", name, formatAmount(*req.Amount), fromMonth, strings.TrimSpace(req.Reason))
	}
}

func formatAmount(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func changeEntryText(fromMonth string, amount, minimal *float64) string {
	b := &strings.Builder{}
	b.WriteString(`{ "from": "` + fromMonth + `-01", "amount": ` + formatAmount(*amount))
	if minimal != nil {
		b.WriteString(`, "minimal_amount": ` + formatAmount(*minimal))
	}
	b.WriteString(" }")
	return b.String()
}

// rewriteAmountChanges edits exactly one category's amount_changes, splicing
// bytes the way setCategoryDate does so the rest of the file — and its
// formatting — stays untouched and the commit diff is a few lines.
func rewriteAmountChanges(src []byte, categoryID, fromMonth string, amount, minimal *float64, remove bool) ([]byte, error) {
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
		for dec.More() {
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
				for dec.More() {
					out, done, err := spliceOneCategory(dec, src, categoryID, fromMonth, amount, minimal, remove)
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
		if err := expectDelim(dec, ']'); err != nil {
			return nil, errorf(CodeUpstream, "budget.json: %v", err)
		}
	}
	return nil, errorf(CodeInvalidRequest, "no category %q in budget.json", categoryID)
}

type changeEntry struct {
	start int
	end   int
}

func spliceOneCategory(dec *json.Decoder, src []byte, categoryID, fromMonth string, amount, minimal *float64, remove bool) ([]byte, bool, error) {
	if err := expectDelim(dec, '{'); err != nil {
		return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
	}
	catStart := int(dec.InputOffset())
	id := ""
	braceEnd := catStart
	keyIndent := ""
	firstKey := true
	acKeyStart := -1
	acArrStart, acArrEnd := -1, -1
	var els []changeEntry
	lastValueEnd := catStart

	for dec.More() {
		keyStart := keyStart(src, int(dec.InputOffset()))
		key, kerr := objectKey(dec)
		if kerr != nil {
			return nil, false, errorf(CodeUpstream, "budget.json: %v", kerr)
		}
		if firstKey {
			keyIndent = string(src[braceEnd:keyStart])
			firstKey = false
		}
		switch key {
		case "id":
			newID, iderr := stringValue(dec)
			if iderr != nil {
				return nil, false, errorf(CodeUpstream, "budget.json: %v", iderr)
			}
			id = newID
		case "amount_changes":
			acKeyStart = keyStart
			if err := expectDelim(dec, '['); err != nil {
				return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
			}
			acArrStart = int(dec.InputOffset())
			for dec.More() {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
				}
				el := changeEntry{start: int(dec.InputOffset()) - len(raw), end: int(dec.InputOffset())}
				var ch budgetdata.AmountChange
				if uerr := json.Unmarshal(raw, &ch); uerr != nil || ch.From == "" {
					return nil, false, errorf(CodeUpstream, "budget.json: amount_changes entry is not a dated amount: %v", uerr)
				}
				els = append(els, el)
			}
			if err := expectDelim(dec, ']'); err != nil {
				return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
			}
			acArrEnd = int(dec.InputOffset()) - 1
		default:
			if err := skipValue(dec); err != nil {
				return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
			}
		}
		lastValueEnd = int(dec.InputOffset())
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, false, errorf(CodeUpstream, "budget.json: %v", err)
	}
	if id != categoryID {
		return nil, false, nil
	}

	if remove {
		if acArrStart < 0 {
			return nil, true, errorf(CodeInvalidRequest, "the category has no amount_changes to remove")
		}
		idx := entryIndexFor(src, els, fromMonth)
		if idx < 0 {
			return nil, true, errorf(CodeNotFound, "no scheduled change for %s in the file", fromMonth)
		}
		if len(els) == 1 {
			return dropWholeKey(src, acKeyStart, acArrEnd), true, nil
		}
		return dropEntry(src, els, idx), true, nil
	}

	elText := changeEntryText(fromMonth, amount, minimal)
	switch {
	case acArrStart < 0:
		return insertKey(src, lastValueEnd, keyIndent, elText), true, nil
	case len(els) == 0:
		return splice(src, acArrStart, acArrEnd, " "+elText+" "), true, nil
	case entryIndexFor(src, els, fromMonth) >= 0:
		e := els[entryIndexFor(src, els, fromMonth)]
		return splice(src, e.start, e.end, elText), true, nil
	default:
		// The new entry takes the indent the first one has, so a multi-line
		// list keeps its shape and a one-line one stays one line.
		return splice(src, els[len(els)-1].end, els[len(els)-1].end, ","+string(src[acArrStart:els[0].start])+elText), true, nil
	}
}

func entryIndexFor(src []byte, els []changeEntry, fromMonth string) int {
	for i, e := range els {
		var ch budgetdata.AmountChange
		if err := json.Unmarshal(src[e.start:e.end], &ch); err != nil {
			continue
		}
		if monthOf(ch.From) == fromMonth {
			return i
		}
	}
	return -1
}

func splice(src []byte, lo, hi int, text string) []byte {
	return append(append(append([]byte{}, src[:lo]...), text...), src[hi:]...)
}

// insertKey adds "amount_changes": [ entry ] to a category that has none yet,
// reusing the whitespace between the brace and the first key, so a one-line
// category gets an inline field and a multi-line one a new line at its own
// indent.
func insertKey(src []byte, lastValueEnd int, keyIndent, elText string) []byte {
	return splice(src, lastValueEnd, lastValueEnd, ","+keyIndent+`"amount_changes": [ `+elText+` ]`)
}

// dropWholeKey removes the "amount_changes" key and its value, plus exactly
// one adjacent comma, whichever side the comma lives on. On the comma-after
// side it also swallows the whitespace run the key sat in, so a multi-line
// file loses the line rather than a value and a gap.
func dropWholeKey(src []byte, keyStart, arrEnd int) []byte {
	lo := keyStart
	hi := arrEnd + 1
	k := hi
	for k < len(src) && isSpaceByte(src[k]) {
		k++
	}
	if k < len(src) && src[k] == ',' {
		hi = k + 1
		lo = whitespaceStart(src, keyStart)
	} else {
		k = lo - 1
		for k > 0 && isSpaceByte(src[k]) {
			k--
		}
		if k >= 0 && src[k] == ',' {
			lo = k
		}
	}
	return splice(src, lo, hi, "")
}

func dropEntry(src []byte, els []changeEntry, idx int) []byte {
	e := els[idx]
	lo, hi := e.start, e.end
	if idx == len(els)-1 {
		k := lo - 1
		for k > 0 && isSpaceByte(src[k]) {
			k--
		}
		if k >= 0 && src[k] == ',' {
			lo = k
		}
	} else {
		// Take the comma after, and the whitespace run the entry sat in before,
		// so a multi-line list loses the entry's line rather than the value
		// and a blank line.
		k := hi
		for k < len(src) && isSpaceByte(src[k]) {
			k++
		}
		if k < len(src) && src[k] == ',' {
			hi = k + 1
		}
		lo = whitespaceStart(src, lo)
	}
	return splice(src, lo, hi, "")
}

// whitespaceStart is the first position of the whitespace run immediately
// before at, or at where at is not preceded by whitespace.
func whitespaceStart(src []byte, at int) int {
	k := at
	for k > 0 && isSpaceByte(src[k-1]) {
		k--
	}
	return k
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

func verifyOnlyAmountChangesChanged(before, after []byte, categoryID string) error {
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(after, &bf); err != nil {
		return errorf(CodeInternal, "the result does not parse: %v", err)
	}
	if err := budgetdata.ValidateBudget(bf); err != nil {
		return errorf(CodeInternal, "the result fails validation: %v", err)
	}

	var a, b any
	if err := json.Unmarshal(before, &a); err != nil {
		return errorf(CodeUpstream, "budget.json does not parse: %v", err)
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return errorf(CodeUpstream, "budget.json does not parse: %v", err)
	}
	stripCategoryAmountChanges(a, categoryID)
	stripCategoryAmountChanges(b, categoryID)
	if !reflect.DeepEqual(a, b) {
		return errorf(CodeInternal, "the result differs from the original by more than one category's amount_changes")
	}
	return nil
}

func stripCategoryAmountChanges(doc any, categoryID string) {
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
				delete(cat, "amount_changes")
			}
		}
	}
}
