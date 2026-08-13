package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func runInvoices(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl invoices: a subcommand is required (extract-paid)")
		return 1
	}
	switch args[0] {
	case "extract-paid":
		return runExtractPaid(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl invoices: unknown subcommand %q\n", args[0])
		return 1
	}
}

type extractedPayment struct {
	Invoice string `json:"invoice"`
	Date    string `json:"date"`
}

type extractedPaidFile struct {
	Schema string             `json:"$schema"`
	Paid   []extractedPayment `json:"paid"`
}

func runExtractPaid(args []string) int {
	dir := "data"
	if len(args) > 0 {
		dir = args[0]
	}

	out := filepath.Join(dir, "paid-invoices.json")
	switch _, err := os.Stat(out); {
	case err == nil:
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl invoices extract-paid: %s already exists — refusing to overwrite it\n", out)
		return 1
	case !errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl invoices extract-paid: %v\n", err)
		return 1
	}

	invoicesPath := filepath.Join(dir, "invoices")
	entries, err := os.ReadDir(invoicesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl invoices extract-paid: read %s: %v\n", invoicesPath, err)
		return 1
	}

	var payments []extractedPayment
	stripped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(invoicesPath, e.Name())
		payment, didStrip, err := extractPaidFrom(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl invoices extract-paid: %s: %v\n", path, err)
			return 1
		}
		if payment != nil {
			payments = append(payments, *payment)
		}
		if didStrip {
			stripped++
			fmt.Printf("stripped paid/annulment from %s\n", path)
		}
	}

	sort.Slice(payments, func(i, j int) bool { return payments[i].Invoice < payments[j].Invoice })

	body, err := json.MarshalIndent(extractedPaidFile{
		Schema: "../schemas/paid-invoices.json",
		Paid:   payments,
	}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl invoices extract-paid: marshal: %v\n", err)
		return 1
	}
	if err := os.WriteFile(out, append(body, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl invoices extract-paid: write %s: %v\n", out, err)
		return 1
	}

	fmt.Printf("wrote %s with %d payment(s); rewrote %d invoice file(s)\n", out, len(payments), stripped)
	return 0
}

func extractPaidFrom(path string) (*extractedPayment, bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}

	members, tail, err := splitTopLevel(src)
	if err != nil {
		return nil, false, err
	}

	var payment *extractedPayment
	var number string
	present := false
	for _, m := range members {
		switch m.key {
		case "number":
			if err := json.Unmarshal(m.value, &number); err != nil {
				return nil, false, fmt.Errorf("number: %w", err)
			}
		case "paid":
			present = true
			var date *string
			if err := json.Unmarshal(m.value, &date); err != nil {
				return nil, false, fmt.Errorf("paid: %w", err)
			}
			if date != nil {
				payment = &extractedPayment{Date: *date}
			}
		case "annulment":
			present = true
		}
	}

	if payment != nil {
		if number == "" {
			return nil, false, errors.New("paid is set but the file has no number field")
		}
		payment.Invoice = number
	}
	if !present {
		return payment, false, nil
	}

	rebuilt := rebuildWithout(members, tail, "paid", "annulment")
	if err := os.WriteFile(path, rebuilt, 0o644); err != nil {
		return nil, false, err
	}
	return payment, true, nil
}

type member struct {
	key   string
	value json.RawMessage
	text  []byte
}

func splitTopLevel(src []byte) (members []member, tail []byte, err error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, errors.New("top level is not a JSON object")
	}

	pos := dec.InputOffset()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected an object key, got %v", tok)
		}

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, nil, err
		}
		end := dec.InputOffset()

		members = append(members, member{key: key, value: value, text: src[pos:end]})

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

func rebuildWithout(members []member, tail []byte, drop ...string) []byte {
	dropped := make(map[string]bool, len(drop))
	for _, k := range drop {
		dropped[k] = true
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, m := range members {
		if dropped[m.key] {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		buf.Write(m.text)
		first = false
	}
	buf.Write(tail)
	return buf.Bytes()
}
