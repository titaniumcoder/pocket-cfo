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

	out, err := json.MarshalIndent(&inv, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	return true, nil
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
