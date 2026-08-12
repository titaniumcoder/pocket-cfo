package actualsdiff

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
)

const (
	Removed        = "removed"
	Mutated        = "mutated"
	CoverageShrank = "coverage-shrank"
)

type Change struct {
	Kind    string
	Subject string
	Detail  string
}

func (c Change) String() string {
	return fmt.Sprintf("%s %s: %s", c.Kind, c.Subject, c.Detail)
}

func Diff(before, after actualsdata.ActualsFile) []Change {
	var changes []Change
	changes = append(changes, transactionChanges(before, after)...)
	changes = append(changes, coverageChanges(before, after)...)
	return changes
}

func transactionChanges(before, after actualsdata.ActualsFile) []Change {
	now := map[string]actualsdata.Transaction{}
	for _, tx := range after.Transactions {
		now[tx.Id] = tx
	}

	var changes []Change
	for _, was := range before.Transactions {
		is, ok := now[was.Id]
		if !ok {
			changes = append(changes, Change{Kind: Removed, Subject: was.Id,
				Detail: fmt.Sprintf("%s %s %.2f is no longer present", was.Date, was.Description, was.Amount)})
			continue
		}
		for _, d := range mutations(was, is) {
			changes = append(changes, Change{Kind: Mutated, Subject: was.Id, Detail: d})
		}
	}
	return changes
}

func mutations(was, is actualsdata.Transaction) []string {
	var out []string
	if was.Date != is.Date {
		out = append(out, fmt.Sprintf("date %s became %s", was.Date, is.Date))
	}
	if was.Amount != is.Amount {
		out = append(out, fmt.Sprintf("amount %.2f became %.2f", was.Amount, is.Amount))
	}
	if was.Account != is.Account {
		out = append(out, fmt.Sprintf("account %q became %q", was.Account, is.Account))
	}
	if deref(was.Category) != deref(is.Category) {
		out = append(out, fmt.Sprintf("category %q became %q", deref(was.Category), deref(is.Category)))
	}
	if deref(was.Ignored) != deref(is.Ignored) {
		out = append(out, fmt.Sprintf("ignored %q became %q", deref(was.Ignored), deref(is.Ignored)))
	}
	if deref(was.Untracked) != deref(is.Untracked) {
		out = append(out, fmt.Sprintf("untracked %q became %q", deref(was.Untracked), deref(is.Untracked)))
	}
	if a, b := splitLabel(was), splitLabel(is); a != b {
		out = append(out, fmt.Sprintf("splits %s became %s", a, b))
	}
	return out
}

func splitLabel(tx actualsdata.Transaction) string {
	if len(tx.Splits) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(tx.Splits))
	for _, s := range tx.Splits {
		parts = append(parts, fmt.Sprintf("%.2f→%s%s%s", s.Amount, deref(s.Category), deref(s.Ignored), deref(s.Untracked)))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func coverageChanges(before, after actualsdata.ActualsFile) []Change {
	wasDays := coveredDays(before)
	isDays := coveredDays(after)

	var accounts []string
	for account := range wasDays {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)

	var changes []Change
	for _, account := range accounts {
		var lost []string
		for day := range wasDays[account] {
			if !isDays[account][day] {
				lost = append(lost, day)
			}
		}
		if len(lost) == 0 {
			continue
		}
		sort.Strings(lost)
		detail := fmt.Sprintf("%d day(s) no longer covered, from %s", len(lost), lost[0])
		if len(lost) > 1 {
			detail = fmt.Sprintf("%d day(s) no longer covered, %s..%s", len(lost), lost[0], lost[len(lost)-1])
		}
		changes = append(changes, Change{Kind: CoverageShrank, Subject: account, Detail: detail})
	}
	return changes
}

func coveredDays(f actualsdata.ActualsFile) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, c := range f.Coverage {
		from, err1 := time.Parse("2006-01-02", c.From)
		to, err2 := time.Parse("2006-01-02", c.To)
		if err1 != nil || err2 != nil {
			continue
		}
		if out[c.Account] == nil {
			out[c.Account] = map[string]bool{}
		}
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			out[c.Account][d.Format("2006-01-02")] = true
		}
	}
	return out
}
