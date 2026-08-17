package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
)

const DefaultAccountsPath = "data/accounts.json"

type RecordBalanceRequest struct {
	Account string  `json:"account"`
	AsOf    string  `json:"as_of"`
	Balance float64 `json:"balance"`
	Note    string  `json:"note,omitempty"`
}

type RecordBalanceResult struct {
	Account       string  `json:"account"`
	Kind          string  `json:"kind"`
	AsOf          string  `json:"as_of"`
	Balance       float64 `json:"balance"`
	Closes        string  `json:"closes"`
	Opens         string  `json:"opens"`
	Readings      int     `json:"readings"`
	SHA           string  `json:"sha"`
	DeployPending bool    `json:"deploy_pending"`
}

func (s *Service) RecordAccountBalance(ctx context.Context, req RecordBalanceRequest) (*RecordBalanceResult, error) {
	account := strings.TrimSpace(req.Account)
	if account == "" {
		return nil, errorf(CodeInvalidRequest, "account is required, spelled exactly as list_accounts spells it")
	}
	if err := checkDay(req.AsOf); err != nil {
		return nil, errorf(CodeInvalidRequest, "as_of %q %s", req.AsOf, err)
	}
	if err := refuseTheDirectorLoan(account); err != nil {
		return nil, err
	}
	if err := refuseAMidMonthReading(req.AsOf); err != nil {
		return nil, err
	}
	if err := refuseAReadingFromTheFuture(req.AsOf, s.now()); err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}

	path := s.accountsPath()
	src, sha, err := s.Store.Get(ctx, path)
	if err != nil {
		if err == ErrNotFound {
			return nil, errorf(CodeNotFound, "%s does not exist — an account is declared by hand, with the pot it belongs to", path)
		}
		if e, ok := err.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "reading %s: %v", path, err)
	}

	var before accountsdata.AccountsFile
	if uerr := json.Unmarshal(src, &before); uerr != nil {
		return nil, errorf(CodeUpstream, "%s does not parse: %v", path, uerr)
	}
	held, found := findAccount(before, account)
	if !found {
		return nil, unknownAccount(account, before)
	}
	if err := refuseASecondReadingInOneMonth(held, req.AsOf); err != nil {
		return nil, err
	}

	reading := accountsdata.Reading{AsOf: req.AsOf, Balance: req.Balance, Note: optional(strings.TrimSpace(req.Note))}
	out, err := appendReading(src, account, reading)
	if err != nil {
		return nil, err
	}
	if err := verifyOnlyTheReadingWasAdded(src, out, account, reading); err != nil {
		return nil, err
	}

	opens := monthAfter(req.AsOf)
	newSHA, perr := s.Store.Put(ctx, path, out, sha, commitMessageFor(req, opens))
	if perr != nil {
		if e, ok := perr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "writing %s: %v", path, perr)
	}
	s.Accounts.Publish(out)

	return &RecordBalanceResult{
		Account: held.Name, Kind: string(held.Kind),
		AsOf: req.AsOf, Balance: req.Balance,
		Closes: monthOf(req.AsOf), Opens: opens,
		Readings: len(held.Balances) + 1,
		SHA:      newSHA, DeployPending: true,
	}, nil
}

func (s *Service) accountsPath() string {
	if s.AccountsPath != "" {
		return s.AccountsPath
	}
	return DefaultAccountsPath
}

func refuseAMidMonthReading(asOf string) error {
	d, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return errorf(CodeInvalidRequest, "as_of %q is not a real date", asOf)
	}
	if accountsdata.ClosesItsMonth(d) {
		return nil
	}
	last := accountsdata.LastDayOf(d)
	return errorf(CodeInvalidRequest,
		"as_of %s is mid-month, and there is no such thing as a mid-month balance here. "+
			"A balance is the CLOSING figure of a month, read at the end of its last day: send %s, which is what %s closed on and what opens %s. "+
			"Recording %s would file the figure under %s anyway, while the rest of that month's spending had not happened yet.",
		asOf, last.Format("2006-01-02"), monthOf(asOf), monthAfter(asOf), asOf, monthOf(asOf))
}

func refuseAReadingFromTheFuture(asOf string, now time.Time) error {
	today := now.Format("2006-01-02")
	if asOf <= today {
		return nil
	}
	return errorf(CodeInvalidRequest,
		"as_of %s has not happened yet — a balance is read off the bank, not projected. Today is %s, so the last month you can close is %s (as_of %s).",
		asOf, today, monthOf(lastClosedDay(now)), lastClosedDay(now))
}

func lastClosedDay(now time.Time) string {
	last := accountsdata.LastDayOf(now)
	if !last.After(now) {
		return last.Format("2006-01-02")
	}
	return accountsdata.LastDayOf(now.AddDate(0, 0, -now.Day())).Format("2006-01-02")
}

func monthAfter(day string) string {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return ""
	}
	return accountsdata.LastDayOf(d).AddDate(0, 0, 1).Format("2006-01")
}

func findAccount(f accountsdata.AccountsFile, name string) (accountsdata.Account, bool) {
	for _, a := range f.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return accountsdata.Account{}, false
}

func unknownAccount(name string, f accountsdata.AccountsFile) error {
	known := make([]string, 0, len(f.Accounts))
	for _, a := range f.Accounts {
		known = append(known, a.Name)
	}
	sort.Strings(known)
	return &Error{
		Code: CodeInvalidRequest,
		Message: fmt.Sprintf("no account called %q — spell it as list_accounts spells it. This never creates an account: which pot it belongs to is a decision, not a guess, so a new one is declared by hand in the data repo",
			name),
		Details: map[string]any{"known_accounts": known},
	}
}

func refuseASecondReadingInOneMonth(a accountsdata.Account, asOf string) error {
	month := monthOf(asOf)
	for _, r := range a.Balances {
		if monthOf(r.AsOf) != month {
			continue
		}
		return &Error{
			Code: CodeConflict,
			Message: fmt.Sprintf("%s was already read for %s (%s, balance %s) — one month closes on one figure, and a reading is never written over. "+
				"If that one is wrong, a human corrects it in the data repo", a.Name, month, r.AsOf, formatBalance(r.Balance)),
			Details: map[string]any{"account": a.Name, "month": month, "as_of": r.AsOf, "balance": r.Balance},
		}
	}
	return nil
}

func commitMessageFor(req RecordBalanceRequest, opens string) string {
	body := fmt.Sprintf("The closing balance of %s, which opens %s.", monthOf(req.AsOf), opens)
	if note := strings.TrimSpace(req.Note); note != "" {
		body = note
	}
	return fmt.Sprintf("feat(accounts): %s read at %s\n\n%s\n", req.Account, req.AsOf, body)
}

func formatBalance(b float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", b), "0"), ".")
}

func renderReading(r accountsdata.Reading) string {
	fields := []string{`"as_of": ` + asJSON(r.AsOf), `"balance": ` + asJSON(r.Balance)}
	if r.Note != nil && *r.Note != "" {
		fields = append(fields, `"note": `+asJSON(*r.Note))
	}
	return "{ " + strings.Join(fields, ", ") + " }"
}

func asJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(out)
}

func appendReading(src []byte, account string, reading accountsdata.Reading) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, errorf(CodeUpstream, "accounts.json: %v", err)
	}
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, errorf(CodeUpstream, "accounts.json: %v", err)
		}
		if key != "accounts" {
			if serr := skipValue(dec); serr != nil {
				return nil, errorf(CodeUpstream, "accounts.json: %v", serr)
			}
			continue
		}
		if derr := expectDelim(dec, '['); derr != nil {
			return nil, errorf(CodeUpstream, "accounts.json: %v", derr)
		}
		for dec.More() {
			out, done, rerr := rewriteAccount(dec, src, account, reading)
			if rerr != nil {
				return nil, rerr
			}
			if done {
				return out, nil
			}
		}
		if derr := expectDelim(dec, ']'); derr != nil {
			return nil, errorf(CodeUpstream, "accounts.json: %v", derr)
		}
	}
	return nil, errorf(CodeInvalidRequest, "no account %q in accounts.json", account)
}

// refuseTheDirectorLoan answers the one wrong guess list_accounts now makes
// possible: the loan is reported beside the accounts, so an agent may try to
// record a balance for it. Saying what it is beats "no such account", which
// reads as a typo and invites a retry.
func refuseTheDirectorLoan(account string) error {
	if !strings.EqualFold(strings.TrimSpace(account), "director's loan") &&
		!strings.EqualFold(strings.TrimSpace(account), KindDirectorLoan) {
		return nil
	}
	return errorf(CodeInvalidRequest,
		"the director's loan is not a bank account and takes no reading here — there is nothing to read it off. "+
			"It is restated by hand in accounts.json, usually once a year with the accountant. Read it with get_director_loan.")
}

func rewriteAccount(dec *json.Decoder, src []byte, account string, reading accountsdata.Reading) (out []byte, done bool, err error) {
	if derr := expectDelim(dec, '{'); derr != nil {
		return nil, false, errorf(CodeUpstream, "accounts.json: %v", derr)
	}

	name := ""
	insertAt, indent := -1, ""
	for dec.More() {
		key, kerr := objectKey(dec)
		if kerr != nil {
			return nil, false, errorf(CodeUpstream, "accounts.json: %v", kerr)
		}
		switch key {
		case "name":
			if name, err = stringValue(dec); err != nil {
				return nil, false, errorf(CodeUpstream, "accounts.json: %v", err)
			}
		case "balances":
			if insertAt, indent, err = afterTheLastReading(dec, src); err != nil {
				return nil, false, errorf(CodeUpstream, "accounts.json: %v", err)
			}
		default:
			if serr := skipValue(dec); serr != nil {
				return nil, false, errorf(CodeUpstream, "accounts.json: %v", serr)
			}
		}
	}
	if derr := expectDelim(dec, '}'); derr != nil {
		return nil, false, errorf(CodeUpstream, "accounts.json: %v", derr)
	}
	if name != account {
		return nil, false, nil
	}
	if insertAt < 0 {
		return nil, false, errorf(CodeValidationFailed,
			"account %q is declared with no readings, so there is nothing to append to — give it a first balance by hand in accounts.json", account)
	}

	var buf bytes.Buffer
	buf.Write(src[:insertAt])
	buf.WriteString("," + indent + renderReading(reading))
	buf.Write(src[insertAt:])
	return buf.Bytes(), true, nil
}

func afterTheLastReading(dec *json.Decoder, src []byte) (insertAt int, indent string, err error) {
	if derr := expectDelim(dec, '['); derr != nil {
		return -1, "", derr
	}
	insertAt = -1
	for dec.More() {
		start := elementStart(src, int(dec.InputOffset()))
		if serr := skipValue(dec); serr != nil {
			return -1, "", serr
		}
		indent = indentBefore(src, start)
		insertAt = trimTrailingSpace(src, int(dec.InputOffset()))
	}
	if derr := expectDelim(dec, ']'); derr != nil {
		return -1, "", derr
	}
	return insertAt, indent, nil
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func elementStart(src []byte, from int) int {
	for i := from; i < len(src); i++ {
		if !isSpace(src[i]) && src[i] != ',' {
			return i
		}
	}
	return from
}

func indentBefore(src []byte, start int) string {
	i := start
	for i > 0 && isSpace(src[i-1]) {
		i--
	}
	return string(src[i:start])
}

func trimTrailingSpace(src []byte, end int) int {
	for end > 0 && isSpace(src[end-1]) {
		end--
	}
	return end
}

func verifyOnlyTheReadingWasAdded(before, after []byte, account string, reading accountsdata.Reading) error {
	var af accountsdata.AccountsFile
	if err := json.Unmarshal(after, &af); err != nil {
		return errorf(CodeInternal, "the result does not satisfy accounts.schema.json: %v", err)
	}
	if err := accountsdata.ValidateAccounts(af); err != nil {
		return errorf(CodeValidationFailed, "the result fails validation: %v", err)
	}
	held, found := findAccount(af, account)
	if !found || len(held.Balances) == 0 || !reflect.DeepEqual(held.Balances[len(held.Balances)-1], reading) {
		return errorf(CodeInternal, "the result does not end on the reading that was sent")
	}

	var a, b any
	if err := json.Unmarshal(before, &a); err != nil {
		return errorf(CodeUpstream, "accounts.json does not parse: %v", err)
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return errorf(CodeInternal, "the result does not parse: %v", err)
	}
	dropLastReading(b, account)
	if !reflect.DeepEqual(a, b) {
		return errorf(CodeInternal, "the result differs from the original by more than one appended reading")
	}
	return nil
}

func dropLastReading(doc any, account string) {
	root, ok := doc.(map[string]any)
	if !ok {
		return
	}
	accounts, ok := root["accounts"].([]any)
	if !ok {
		return
	}
	for _, raw := range accounts {
		a, ok := raw.(map[string]any)
		if !ok || a["name"] != account {
			continue
		}
		balances, ok := a["balances"].([]any)
		if !ok || len(balances) == 0 {
			continue
		}
		a["balances"] = balances[:len(balances)-1]
	}
}
