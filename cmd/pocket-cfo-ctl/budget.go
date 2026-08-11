package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

// runBudget dispatches `pocket-cfo-ctl budget <subcommand>`.
func runBudget(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl budget: a subcommand is required (ids)")
		return 1
	}
	switch args[0] {
	case "ids":
		return runBudgetIDs(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl budget: unknown subcommand %q\n", args[0])
		return 1
	}
}

// runBudgetIDs implements `pocket-cfo-ctl budget ids [dataDir] [--dry-run]`:
// fills a stable id into every budget.json category that lacks one. Run it
// once to migrate, and again whenever a category is added by hand — it skips
// categories that already have one, so it is idempotent.
//
// It reads budget.json as raw JSON rather than through budgetdata.BudgetFile,
// because `id` is a required field: the generated unmarshaller rejects exactly
// the un-migrated file this command exists to fix. (Same reason
// `invoices extract-paid` reads raw.) The *result* is checked against the
// generated type, which is the direction that works.
func runBudgetIDs(args []string) int {
	fs := flag.NewFlagSet("budget ids", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the ids that would be assigned, without writing")

	flagArgs, positional := splitFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	dir := "data"
	if len(positional) > 0 {
		dir = positional[0]
	}
	path := filepath.Join(dir, "budget.json")

	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl budget ids:", err)
		return 1
	}

	cats, err := scanCategories(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl budget ids: %s: %v\n", path, err)
		return 1
	}
	if err := assignIDs(cats); err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl budget ids:", err)
		return 1
	}

	missing := 0
	for _, c := range cats {
		verb := "keep  "
		if !c.hadID {
			verb = "assign"
			missing++
		}
		fmt.Printf("%s %-28s %-24s %s\n", verb, c.group, c.name, c.id)
	}
	if *dryRun {
		fmt.Println("(dry run — nothing written)")
		return 0
	}
	if missing == 0 {
		fmt.Printf("%s already has an id on every category; nothing to do\n", path)
		return 0
	}

	out := insertIDs(src, cats)
	if err := verifyOnlyIDsChanged(src, out); err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl budget ids: refusing to write —", err)
		return 1
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl budget ids:", err)
		return 1
	}

	fmt.Printf("wrote %s (%d id(s) assigned)\n", path, missing)
	return 0
}

// rawCat is one category as it appears in the file, before any struct is involved.
type rawCat struct {
	group string
	name  string
	id    string
	hadID bool
	start int // byte offset just past the category object's opening brace
}

// assignIDs gives every category without an id a fresh UUID. An id already
// present is kept verbatim: an id is a promise that outlives a rename, so it
// is never recomputed from anything.
//
// A UUID rather than a slug of the name, deliberately. A slug looks derived
// from the category, so renaming "Groceries" to "Food" invites someone to
// tidy the id to match — orphaning every transaction ever matched to it. An
// opaque id has nothing to tidy.
func assignIDs(cats []rawCat) error {
	taken := map[string]bool{}
	for _, c := range cats {
		if c.hadID {
			taken[c.id] = true
		}
	}
	for i := range cats {
		if cats[i].hadID {
			continue
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		for taken[id] { // astronomically unlikely; cheap to be certain
			if id, err = newUUID(); err != nil {
				return err
			}
		}
		taken[id] = true
		cats[i].id = id
	}
	return nil
}

// newUUID returns a random (version 4) UUID. Hand-rolled over crypto/rand
// rather than pulling in a dependency for sixteen bytes and a format string —
// see AGENTS.md's stdlib-first rule.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating an id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// insertIDs splices `"id": "…",` into each category object that needs one,
// leaving every other byte exactly as it was.
//
// budget.json is hand-maintained with deliberate compact formatting — one
// category per line, keys in a chosen order. Round-tripping it through
// json.MarshalIndent would explode every category across six lines and sort
// the keys, burying the ids (the only thing worth reviewing) in a diff that
// touches the whole file, permanently. So this edits bytes instead.
func insertIDs(src []byte, cats []rawCat) []byte {
	out := src
	// Back to front, so each insertion leaves the earlier offsets valid.
	for i := len(cats) - 1; i >= 0; i-- {
		if cats[i].hadID {
			continue
		}
		at := cats[i].start
		// Reuse whatever whitespace already follows the brace, so a one-line
		// category stays on one line and an indented one stays indented.
		ins := leadingSpace(src[at:]) + `"id": "` + cats[i].id + `",`
		out = append(out[:at:at], append([]byte(ins), out[at:]...)...)
	}
	return out
}

func leadingSpace(b []byte) string {
	for i, c := range b {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return string(b[:i])
		}
	}
	return ""
}

// scanCategories walks the raw JSON and returns every groups[].categories[]
// entry in file order, with the byte offset just past its opening brace.
func scanCategories(src []byte) ([]rawCat, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}

	var cats []rawCat
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, err
		}
		if key != "groups" {
			if err := skipValue(dec); err != nil {
				return nil, err
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return nil, err
		}
		for dec.More() { // one group
			if err := expectDelim(dec, '{'); err != nil {
				return nil, err
			}
			groupName := ""
			var mine []int // indices of this group's categories, for back-filling the name
			for dec.More() {
				gkey, err := objectKey(dec)
				if err != nil {
					return nil, err
				}
				switch gkey {
				case "name":
					if groupName, err = stringValue(dec); err != nil {
						return nil, err
					}
				case "categories":
					if err := expectDelim(dec, '['); err != nil {
						return nil, err
					}
					for dec.More() {
						c, err := scanCategory(dec)
						if err != nil {
							return nil, err
						}
						mine = append(mine, len(cats))
						cats = append(cats, c)
					}
					if err := expectDelim(dec, ']'); err != nil {
						return nil, err
					}
				default:
					if err := skipValue(dec); err != nil {
						return nil, err
					}
				}
			}
			if err := expectDelim(dec, '}'); err != nil {
				return nil, err
			}
			// Back-filled, because "name" may follow "categories" in the file.
			for _, i := range mine {
				cats[i].group = groupName
			}
		}
		if err := expectDelim(dec, ']'); err != nil {
			return nil, err
		}
	}
	return cats, nil
}

func scanCategory(dec *json.Decoder) (rawCat, error) {
	if err := expectDelim(dec, '{'); err != nil {
		return rawCat{}, err
	}
	c := rawCat{start: int(dec.InputOffset())}
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return rawCat{}, err
		}
		switch key {
		case "name":
			if c.name, err = stringValue(dec); err != nil {
				return rawCat{}, err
			}
		case "id":
			if c.id, err = stringValue(dec); err != nil {
				return rawCat{}, err
			}
			c.hadID = true
		default:
			if err := skipValue(dec); err != nil {
				return rawCat{}, err
			}
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return rawCat{}, err
	}
	return c, nil
}

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

// verifyOnlyIDsChanged proves the edit did exactly one thing, before anything
// is overwritten. Byte surgery on a file this important is only safe if it
// checks its own work.
func verifyOnlyIDsChanged(before, after []byte) error {
	// The result must satisfy the real schema and the real business rules.
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(after, &bf); err != nil {
		return fmt.Errorf("the result does not satisfy budget.schema.json: %w", err)
	}
	if err := budgetdata.ValidateBudget(bf); err != nil {
		return fmt.Errorf("the result fails validation: %w", err)
	}

	// And it must differ from the original in nothing but category ids. Both
	// sides go through map[string]any, since the before state deliberately
	// can't be parsed by the generated type — and ids are stripped from both,
	// since the file may already carry some from an earlier run.
	var a, b any
	if err := json.Unmarshal(before, &a); err != nil {
		return err
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return err
	}
	stripCategoryIDs(a)
	stripCategoryIDs(b)
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("the result differs from the original by more than category ids")
	}
	return nil
}

// stripCategoryIDs deletes every groups[].categories[].id from a decoded
// document, so it can be compared against one that never had them.
func stripCategoryIDs(doc any) {
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
			if cat, ok := c.(map[string]any); ok {
				delete(cat, "id")
			}
		}
	}
}
