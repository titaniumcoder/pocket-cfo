package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
)

const DefaultConfigPath = "config.json"

const (
	FileBudget   = "budget.json"
	FileAccounts = "accounts.json"
	FileConfig   = "config.json"
)

type DataFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

type FileWriteRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Reason  string `json:"reason"`
}

type FileWriteResult struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	SHA           string `json:"sha"`
	ChangedLines  int    `json:"changed_lines"`
	Diff          string `json:"diff"`
	Unchanged     bool   `json:"unchanged,omitempty"`
	DeployPending bool   `json:"deploy_pending"`
}

func DataFileNames() []string {
	return []string{FileBudget, FileAccounts, FileConfig}
}

func (s *Service) dataFilePath(name string) (string, error) {
	switch name {
	case FileBudget:
		return s.budgetPath(), nil
	case FileAccounts:
		return s.accountsPath(), nil
	case FileConfig:
		if s.ConfigPath != "" {
			return s.ConfigPath, nil
		}
		return DefaultConfigPath, nil
	}
	names := DataFileNames()
	sort.Strings(names)
	return "", errorf(CodeInvalidRequest, "no data file named %q — the files this API serves are %s", name, strings.Join(names, ", "))
}

func (s *Service) ReadDataFile(ctx context.Context, name string) (*DataFile, error) {
	path, err := s.dataFilePath(name)
	if err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "the data repo is not configured, so files cannot be read from it")
	}
	content, sha, gerr := s.Store.Get(ctx, path)
	if gerr == ErrNotFound {
		return nil, errorf(CodeNotFound, "%s does not exist in the data repo", path)
	}
	if gerr != nil {
		return nil, errorf(CodeUpstream, "reading %s: %v", path, gerr)
	}
	return &DataFile{Name: name, Path: path, Content: string(content), SHA: sha}, nil
}

func (s *Service) WriteDataFile(ctx context.Context, req FileWriteRequest) (*FileWriteResult, error) {
	path, err := s.dataFilePath(req.Name)
	if err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, errorf(CodeInvalidRequest, "reason is required — it becomes the commit message")
	}
	content := []byte(req.Content)
	if !json.Valid(content) {
		return nil, errorf(CodeValidationFailed, "%s must be valid JSON", req.Name)
	}
	if err := validateDataFile(req.Name, content); err != nil {
		return nil, &Error{Code: CodeValidationFailed, Message: fmt.Sprintf("%s would not load: %v", req.Name, err)}
	}
	before, sha, gerr := s.Store.Get(ctx, path)
	if gerr != nil && gerr != ErrNotFound {
		return nil, errorf(CodeUpstream, "reading %s: %v", path, gerr)
	}
	if strings.TrimSpace(string(before)) == strings.TrimSpace(req.Content) {
		return &FileWriteResult{Name: req.Name, Path: path, SHA: sha, Unchanged: true}, nil
	}
	diff, changed := LineDiff(string(before), req.Content)
	if !strings.HasSuffix(req.Content, "\n") {
		content = append(content, '\n')
	}
	message := fmt.Sprintf("chore(data): %s — %s\n", req.Name, strings.TrimSpace(req.Reason))
	newSHA, perr := s.Store.Put(ctx, path, content, sha, message)
	if perr != nil {
		if e, ok := perr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "committing %s: %v", path, perr)
	}
	s.Publish(path, content)
	return &FileWriteResult{Name: req.Name, Path: path, SHA: newSHA, ChangedLines: changed, Diff: diff, DeployPending: true}, nil
}

func validateDataFile(name string, content []byte) error {
	switch name {
	case FileBudget:
		var f budgetdata.BudgetFile
		if err := json.Unmarshal(content, &f); err != nil {
			return err
		}
		return budgetdata.ValidateBudget(f)
	case FileAccounts:
		var f accountsdata.AccountsFile
		if err := json.Unmarshal(content, &f); err != nil {
			return err
		}
		return accountsdata.ValidateAccounts(f)
	case FileConfig:
		_, err := financeconfig.ParseFileConfig(content)
		return err
	}
	return nil
}
