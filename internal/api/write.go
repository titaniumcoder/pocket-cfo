package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdiff"
)

// DefaultActualsPrefix is where months live in the data repo, which nests its
// hand-edited files under data/.
const DefaultActualsPrefix = "data/actuals"

// PutRequest is the whole-month submission. Anything omitted from Document is
// a removal, which is the point: a per-transaction API would mean a redeploy
// per transaction, since the data repo deploys on commit.
type PutRequest struct {
	Document      json.RawMessage `json:"document"`
	BaseSHA       string          `json:"base_sha"`
	AllowRemovals string          `json:"allow_removals"`
}

// PutResult is what the caller gets back.
type PutResult struct {
	Month         string   `json:"month"`
	SHA           string   `json:"sha"`
	DeployPending bool     `json:"deploy_pending"`
	Applied       string   `json:"applied_overrides,omitempty"`
	Changes       []string `json:"accepted_changes,omitempty"`
}

// PutActuals commits one month, rejecting at the first stage that fails and
// reaching GitHub only once everything local has passed.
//
// The before-state comes from GitHub, never from DATA_DIR: the mounted
// checkout lags the repo by one deploy, so diffing against it would let a
// write silently erase the commit that is currently deploying.
func (s *Service) PutActuals(ctx context.Context, month string, req PutRequest) (*PutResult, error) {
	if _, _, err := ParseMonth(month); err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}

	doc, err := decodeDocument(req.Document)
	if err != nil {
		return nil, err
	}
	if doc.Month != month {
		return nil, errorf(CodeInvalidRequest, "document month %q does not match the path month %q", doc.Month, month)
	}

	knownIDs, err := s.knownCategoryIDs(ctx)
	if err != nil {
		return nil, err
	}
	if verr := actualsdata.ValidateActuals(doc, month, knownIDs); verr != nil {
		return nil, &Error{Code: CodeValidationFailed, Message: verr.Error()}
	}

	path := s.actualsPath(month)
	before, sha, gerr := s.Store.Get(ctx, path)
	switch {
	case gerr == ErrNotFound:
		before, sha = nil, ""
	case gerr != nil:
		if e, ok := gerr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "reading %s: %v", path, gerr)
	}

	if req.BaseSHA != sha {
		return nil, &Error{
			Code:    CodeConflict,
			Message: fmt.Sprintf("%s has moved on; re-read it and merge rather than retrying", path),
			Details: map[string]string{"current_sha": sha},
		}
	}

	changes, err := changesAgainst(before, doc)
	if err != nil {
		return nil, err
	}
	if len(changes) > 0 && strings.TrimSpace(req.AllowRemovals) == "" {
		return nil, &Error{
			Code:    CodeWouldRemove,
			Message: "this would destroy recorded data; resubmit with allow_removals explaining why",
			Details: changes,
		}
	}

	// Commit the canonical re-marshal, never the raw body: unvalidated keys
	// can then never reach the repo, and the diff a human reads in git log is
	// stable between submissions.
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, errorf(CodeInternal, "re-marshalling the document: %v", err)
	}
	body = append(body, '\n')

	newSHA, perr := s.Store.Put(ctx, path, body, sha, commitMessage(doc, req.AllowRemovals))
	if perr != nil {
		if e, ok := perr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "writing %s: %v", path, perr)
	}

	return &PutResult{
		Month: month, SHA: newSHA, DeployPending: true,
		Applied: strings.TrimSpace(req.AllowRemovals),
		Changes: changes,
	}, nil
}

// decodeDocument rejects unknown keys, so a typo'd field is an error rather
// than silently dropped data.
func decodeDocument(raw json.RawMessage) (actualsdata.ActualsFile, error) {
	if len(raw) == 0 {
		return actualsdata.ActualsFile{}, errorf(CodeInvalidRequest, "document is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc actualsdata.ActualsFile
	if err := dec.Decode(&doc); err != nil {
		return actualsdata.ActualsFile{}, errorf(CodeInvalidRequest, "document: %v", err)
	}
	return doc, nil
}

func changesAgainst(before []byte, after actualsdata.ActualsFile) ([]string, error) {
	if len(before) == 0 {
		return nil, nil // a brand-new month removes nothing
	}
	var prev actualsdata.ActualsFile
	if err := json.Unmarshal(before, &prev); err != nil {
		return nil, errorf(CodeUpstream, "the committed month does not parse: %v", err)
	}
	var out []string
	for _, c := range actualsdiff.Diff(prev, after) {
		out = append(out, c.String())
	}
	return out, nil
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

func (s *Service) actualsPath(month string) string {
	prefix := s.ActualsPrefix
	if prefix == "" {
		prefix = DefaultActualsPrefix
	}
	return strings.TrimSuffix(prefix, "/") + "/" + month + ".json"
}

// commitMessage is a Conventional Commit, with any override recorded as a
// trailer so git log says why something disappeared.
func commitMessage(doc actualsdata.ActualsFile, allowRemovals string) string {
	through := ""
	for _, c := range doc.Coverage {
		if through == "" || c.To > through {
			through = c.To
		}
	}
	subject := fmt.Sprintf("feat(actuals): reconcile %s", doc.Month)
	if through != "" {
		subject += " through " + through
	}

	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "%d transaction(s), %d coverage range(s).\n", len(doc.Transactions), len(doc.Coverage))
	if reason := strings.TrimSpace(allowRemovals); reason != "" {
		fmt.Fprintf(&b, "\nAllow-Removals: %s\n", reason)
	}
	return b.String()
}
