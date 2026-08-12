package api

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdiff"
)

// DefaultActualsPrefix is where months live in the data repo, which nests its
// hand-edited files under data/.
const DefaultActualsPrefix = "data/actuals"

// loadMonth reads one month as it stands in git, returning an empty document
// for a month that has never been committed.
//
// The before-state comes from GitHub, never from DATA_DIR: the mounted
// checkout lags the repo by one deploy, so merging into it would silently
// erase the commit that is currently deploying.
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

// marshalMonth renders a document the one canonical way, so the diff a human
// reads in git log reflects the change and not the formatter.
//
// Keys are written in schema order — id, date, description, amount, account,
// then whichever disposition applies — rather than in the generated struct's
// order, which go-jsonschema emits alphabetically. That is not cosmetic: with
// alphabetical keys, appending one line rewrites the key order of every other
// line in the file, and a reviewer opening the commit to check that nothing
// was destroyed is handed a diff where everything changed. The order here
// matches what the files already contain, so adding a line touches that line
// and nothing else.
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
		if len(tx.Splits) > 0 {
			splits := make([]orderedObject, 0, len(tx.Splits))
			for _, s := range tx.Splits {
				so := orderedObject{{"amount", s.Amount}}
				so = appendIfSet(so, "category", s.Category)
				so = appendIfSet(so, "ignored", s.Ignored)
				so = appendIfSet(so, "untracked", s.Untracked)
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

// field is one key/value pair; orderedObject is the object that keeps them in
// the order they were written rather than the order a map would pick.
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

// appendIfSet leaves an unset optional field out entirely rather than writing
// a null, which the schema's additionalProperties:false would reject on read.
func appendIfSet(o orderedObject, key string, value *string) orderedObject {
	if value == nil || *value == "" {
		return o
	}
	return append(o, field{key, *value})
}

// refuseDestruction is the backstop under both write endpoints: neither can
// express a removal, so if the diff engine sees one, the code has a bug and
// the commit must not happen. Cheaper than trusting that every future edit to
// the merge logic stays append-only, and it is the one guarantee these
// endpoints are sold on.
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

// knownCategoryIDs is the set a transaction's category must be drawn from. A
// category that no longer resolves fails a write loudly rather than silently
// dropping the money out of every total.
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
