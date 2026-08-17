package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/translate"
)

func runTranslate(_ []string) int {
	apiKey := os.Getenv("DEEPL_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl translate: DEEPL_API_KEY is not set (see .envrc.example)")
		return 1
	}
	client := &translate.Client{APIKey: apiKey, HTTPClient: &http.Client{Timeout: 15 * time.Second}}

	entries, err := os.ReadDir(invoicesDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl translate:", err)
		return 1
	}

	failed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(invoicesDir, e.Name())
		changed, err := translateOne(context.Background(), client, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl translate: %s: %v\n", e.Name(), err)
			failed++
			continue
		}
		if changed {
			fmt.Printf("translated %s\n", path)
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl translate: %d invoice(s) failed\n", failed)
		return 1
	}
	return 0
}

func translateOne(ctx context.Context, client *translate.Client, path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}
	var inv invoice.InvoiceJson
	if err := json.Unmarshal(b, &inv); err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}
	if inv.Status != invoice.InvoiceJsonStatusDraft {
		return false, nil
	}

	changed := false
	for i := range inv.Lines {
		c, err := fillBg(ctx, client, &inv.Lines[i].Description, inv.Language)
		if err != nil {
			return false, fmt.Errorf("line %d description: %w", i+1, err)
		}
		changed = changed || c
	}
	for i := range inv.Discounts {
		c, err := fillBg(ctx, client, &inv.Discounts[i].Label, inv.Language)
		if err != nil {
			return false, fmt.Errorf("discount %d label: %w", i+1, err)
		}
		changed = changed || c
	}

	if !changed {
		return false, nil
	}

	out, err := withTranslations(b, &inv)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	return true, nil
}

func withTranslations(raw []byte, inv *invoice.InvoiceJson) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	setBg := func(container any, i int, key string, ls invoice.LocalizedString) {
		if ls.Bg == nil {
			return
		}
		list, ok := container.([]any)
		if !ok || i >= len(list) {
			return
		}
		item, ok := list[i].(map[string]any)
		if !ok {
			return
		}
		field, ok := item[key].(map[string]any)
		if !ok {
			return
		}
		field["bg"] = *ls.Bg
	}

	for i, l := range inv.Lines {
		setBg(doc["lines"], i, "description", l.Description)
	}
	for i, d := range inv.Discounts {
		setBg(doc["discounts"], i, "label", d.Label)
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return append(out, '\n'), nil
}

func fillBg(ctx context.Context, client *translate.Client, ls *invoice.LocalizedString, lang invoice.InvoiceJsonLanguage) (bool, error) {
	if ls.Bg != nil && *ls.Bg != "" {
		return false, nil
	}
	if lang == invoice.InvoiceJsonLanguageBg {
		return false, nil
	}
	primary, ok := ls.Get(lang)
	if !ok || primary == "" {
		return false, nil
	}
	translated, err := client.Translate(ctx, primary, string(lang), "bg")
	if err != nil {
		return false, err
	}
	ls.Bg = &translated
	return true, nil
}
