// Package actualsdiff compares two versions of one month's recorded spending
// and reports anything that would disappear or change underneath you.
//
// The risk this exists for isn't a malformed file — validation catches that.
// It's a subtly *smaller* one: August is rebuilt from a statement covering
// only the last two weeks, submitted, and the first two weeks quietly cease
// to exist. Every figure still adds up. Nothing looks wrong.
package actualsdiff

import (
	"fmt"
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
)

// Kind values for Change.
const (
	Removed        = "removed"
	Mutated        = "mutated"
	CoverageShrank = "coverage-shrank"
)

// Change is one thing the incoming document would destroy.
type Change struct {
	Kind   string
	ID     string // transaction id, or account name for a coverage regression
	Detail string
}

func (c Change) String() string {
	return fmt.Sprintf("%s %s: %s", c.Kind, c.ID, c.Detail)
}

// Diff reports what `after` would remove or rewrite relative to `before`.
// Adding transactions, extending coverage and a brand-new month are always
// clean — the common case never trips it.
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
			changes = append(changes, Change{Kind: Removed, ID: was.Id,
				Detail: fmt.Sprintf("%s %s %.2f is no longer present", was.Date, was.Description, was.Amount)})
			continue
		}
		for _, d := range mutations(was, is) {
			changes = append(changes, Change{Kind: Mutated, ID: was.Id, Detail: d})
		}
	}
	return changes
}

// mutations lists the fields that changed under a stable id. description is
// deliberately not one of them: correcting a mangled statement line is
// legitimate, and it feeds no figure.
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
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// coverageChanges compares covered *days* per account rather than ranges.
// A range-by-range comparison reports a false positive the moment a second
// weekly import merges two adjacent ranges into one — which is precisely what
// the normal workflow does.
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
		changes = append(changes, Change{Kind: CoverageShrank, ID: account, Detail: detail})
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
