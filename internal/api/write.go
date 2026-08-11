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
