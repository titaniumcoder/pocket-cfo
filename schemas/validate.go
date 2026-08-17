package schemas

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

type ID string

const (
	Invoice      ID = "invoice.json"
	Recipient    ID = "recipient.json"
	Issuer       ID = "issuer.json"
	Notes        ID = "notes.json"
	Users        ID = "users.json"
	PaidInvoices ID = "paid-invoices.json"
)

func Validate(id ID, raw []byte) error {
	resolved, err := Resolved(id)
	if err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if err := resolved.Validate(doc); err != nil {
		return fmt.Errorf("does not match %s: %w", id, err)
	}
	return nil
}

func Resolved(id ID) (*jsonschema.Resolved, error) {
	compilers.mu.Lock()
	once, ok := compilers.byID[id]
	if !ok {
		once = &compiled{}
		compilers.byID[id] = once
	}
	compilers.mu.Unlock()

	once.once.Do(func() { once.resolved, once.err = compile(id) })
	return once.resolved, once.err
}

type compiled struct {
	once     sync.Once
	resolved *jsonschema.Resolved
	err      error
}

var compilers = struct {
	mu   sync.Mutex
	byID map[ID]*compiled
}{byID: map[ID]*compiled{}}

func compile(id ID) (*jsonschema.Resolved, error) {
	raw, err := FS.ReadFile(string(id))
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", id, err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("schema %s: %w", id, err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", id, err)
	}
	return resolved, nil
}
