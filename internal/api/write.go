package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdiff"
)

const DefaultActualsPrefix = "data/actuals"

func (s *Service) loadMonth(ctx context.Context, month string) (doc actualsdata.ActualsFile, raw []byte, sha string, err error) {
	path := s.actualsPath(month)
	raw, sha, gerr := s.Store.Get(ctx, path)
	switch {
	case gerr == ErrNotFound:
		return actualsdata.ActualsFile{Month: month}, nil, "", nil
	case gerr != nil:
		if e, ok := gerr.(*Error); ok {
			return doc, nil, "", e
		}
		return doc, nil, "", errorf(CodeUpstream, "reading %s: %v", path, gerr)
	}
	if uerr := json.Unmarshal(raw, &doc); uerr != nil {
		return doc, nil, "", errorf(CodeUpstream, "the committed %s does not parse: %v", path, uerr)
	}
	return doc, raw, sha, nil
}

func marshalMonth(doc actualsdata.ActualsFile) ([]byte, error) {
	root := orderedObject{}
	if doc.Schema != nil {
		root = append(root, field{"$schema", *doc.Schema})
	}
	root = append(root, field{"month", doc.Month})

	coverage := make([]orderedObject, 0, len(doc.Coverage))
	for _, c := range doc.Coverage {
		coverage = append(coverage, orderedObject{
			{"account", c.Account}, {"from", c.From}, {"to", c.To}, {"imported_at", c.ImportedAt},
		})
	}
	root = append(root, field{"coverage", coverage})

	transactions := make([]orderedObject, 0, len(doc.Transactions))
	for _, tx := range doc.Transactions {
		o := orderedObject{
			{"id", tx.Id}, {"date", tx.Date}, {"description", tx.Description},
			{"amount", tx.Amount}, {"account", tx.Account},
		}
		o = appendIfSet(o, "category", tx.Category)
		o = appendIfSet(o, "ignored", tx.Ignored)
		o = appendIfSet(o, "untracked", tx.Untracked)
		o = appendMovementIfSet(o, tx.Movement)
		if len(tx.Splits) > 0 {
			splits := make([]orderedObject, 0, len(tx.Splits))
			for _, s := range tx.Splits {
				so := orderedObject{{"amount", s.Amount}}
				so = appendIfSet(so, "category", s.Category)
				so = appendIfSet(so, "ignored", s.Ignored)
				so = appendIfSet(so, "untracked", s.Untracked)
				so = appendMovementIfSet(so, s.Movement)
				splits = append(splits, so)
			}
			o = append(o, field{"splits", splits})
		}
		transactions = append(transactions, o)
	}
	root = append(root, field{"transactions", transactions})

	compact, err := json.Marshal(root)
	if err != nil {
		return nil, errorf(CodeInternal, "re-marshalling %s: %v", doc.Month, err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, errorf(CodeInternal, "indenting %s: %v", doc.Month, err)
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

type field struct {
	key   string
	value any
}

type orderedObject []field

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(f.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		value, err := json.Marshal(f.value)
		if err != nil {
			return nil, err
		}
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func appendIfSet(o orderedObject, key string, value *string) orderedObject {
	if value == nil || *value == "" {
		return o
	}
	return append(o, field{key, *value})
}

// appendMovementIfSet exists because this function rebuilds the whole
// document field by field: a field it does not know about is not preserved,
// it is dropped from every line in the month.
func appendMovementIfSet(o orderedObject, m *actualsdata.Movement) orderedObject {
	if m == nil || *m == "" {
		return o
	}
	return append(o, field{"movement", string(*m)})
}

var (
	knownRootKeys     = map[string]bool{"$schema": true, "month": true, "coverage": true, "transactions": true}
	knownCoverageKeys = map[string]bool{"account": true, "from": true, "to": true, "imported_at": true}
	knownLineKeys     = map[string]bool{
		"id": true, "date": true, "description": true, "amount": true, "account": true,
		"category": true, "ignored": true, "untracked": true, "movement": true, "splits": true,
	}
	knownSplitKeys = map[string]bool{
		"amount": true, "category": true, "ignored": true, "untracked": true, "movement": true,
	}
)

func refuseUnknownFields(raw []byte, month string) error {
	if len(raw) == 0 {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return errorf(CodeUpstream, "the committed %s does not parse: %v", month, err)
	}

	var unknown []string
	note := func(where, key string) { unknown = append(unknown, where+"."+key) }

	for k := range doc {
		if !knownRootKeys[k] {
			note(month, k)
		}
	}
	for i, c := range arrayOf(doc["coverage"]) {
		for k := range c {
			if !knownCoverageKeys[k] {
				note(fmt.Sprintf("coverage %d", i+1), k)
			}
		}
	}
	for i, tx := range arrayOf(doc["transactions"]) {
		where := fmt.Sprintf("transaction %d", i+1)
		if id, ok := tx["id"].(string); ok {
			where = "transaction " + id
		}
		for k := range tx {
			if !knownLineKeys[k] {
				note(where, k)
			}
		}
		for j, sp := range arrayOf(tx["splits"]) {
			for k := range sp {
				if !knownSplitKeys[k] {
					note(fmt.Sprintf("%s split %d", where, j+1), k)
				}
			}
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return &Error{
		Code:    CodeInternal,
		Message: "refusing to write: " + month + " carries fields this endpoint does not know, and rewriting the file would drop them. Update the API to understand them first.",
		Details: unknown,
	}
}

func arrayOf(v any) []map[string]any {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func refuseDestruction(prev, next actualsdata.ActualsFile, allowMutation bool) error {
	var destructive []string
	for _, c := range actualsdiff.Diff(prev, next) {
		if c.Kind == actualsdiff.Mutated && allowMutation {
			continue
		}
		destructive = append(destructive, c.String())
	}
	if len(destructive) == 0 {
		return nil
	}
	return &Error{
		Code:    CodeInternal,
		Message: "refusing to write: this would have destroyed recorded data, which neither endpoint is allowed to do",
		Details: destructive,
	}
}

func (s *Service) knownCategoryIDs(ctx context.Context) (map[string]bool, error) {
	idx, err := s.Budget.CategoryIndex(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	out := make(map[string]bool, len(idx))
	for id := range idx {
		out[id] = true
	}
	return out, nil
}

func (s *Service) actualsPath(month string) string {
	prefix := s.ActualsPrefix
	if prefix == "" {
		prefix = DefaultActualsPrefix
	}
	return strings.TrimSuffix(prefix, "/") + "/" + month + ".json"
}
