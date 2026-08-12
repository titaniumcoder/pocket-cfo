package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdiff"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

func runActuals(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl actuals: a subcommand is required (validate, status, categories)")
		return 1
	}
	switch args[0] {
	case "validate":
		return runActualsValidate(args[1:])
	case "status":
		return runActualsStatus(args[1:])
	case "categories":
		return runActualsCategories(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl actuals: unknown subcommand %q\n", args[0])
		return 1
	}
}

func runActualsValidate(args []string) int {
	fs := flag.NewFlagSet("actuals validate", flag.ContinueOnError)
	baseRef := fs.String("base-ref", "", "git revision to compare against (e.g. HEAD, origin/main)")
	allowRemovals := fs.String("allow-removals", "", "reason for accepting removals; never optional and never a bare flag, so git log records why")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "data"
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	months, err := actualsMonths(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl actuals validate:", err)
		return 1
	}
	if len(months) == 0 {
		fmt.Println("pocket-cfo-ctl actuals validate: no months reconciled yet")
		return 0
	}

	knownIDs := budgetCategoryIDs(dir)

	problems := 0
	for _, m := range months {
		af, err := readActuals(m.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl actuals validate: %s: %v\n", m.path, err)
			problems++
			continue
		}
		if err := actualsdata.ValidateActuals(af, m.key, knownIDs); err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl actuals validate: %s: %v\n", m.path, err)
			problems++
			continue
		}
		if *baseRef == "" {
			continue
		}
		problems += diffAgainstRef(dir, m, af, *baseRef, *allowRemovals)
	}

	if problems > 0 {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl actuals validate: %d problem(s) found\n", problems)
		return 1
	}
	fmt.Println("pocket-cfo-ctl actuals validate: OK")
	return 0
}

func diffAgainstRef(dir string, m monthFile, af actualsdata.ActualsFile, ref, allowRemovals string) int {
	rel := filepath.ToSlash(filepath.Join("actuals", filepath.Base(m.path)))
	old, ok, err := gitShow(dir, ref, rel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl actuals validate: %s at %s: %v\n", rel, ref, err)
		return 1
	}
	if !ok {
		return 0
	}
	var before actualsdata.ActualsFile
	if err := json.Unmarshal(old, &before); err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl actuals validate: %s at %s: %v\n", rel, ref, err)
		return 1
	}

	changes := actualsdiff.Diff(before, af)
	if len(changes) == 0 {
		return 0
	}
	if reason := removalReason(dir, ref, allowRemovals); reason != "" {
		fmt.Printf("%s: %d change(s) accepted — %s\n", rel, len(changes), reason)
		for _, c := range changes {
			fmt.Printf("  %s\n", c)
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "pocket-cfo-ctl actuals validate: %s would destroy recorded data:\n", rel)
	for _, c := range changes {
		fmt.Fprintf(os.Stderr, "  %s\n", c)
	}
	fmt.Fprintln(os.Stderr, "  re-run with --allow-removals \"<reason>\", or add an Allow-Removals: <reason> trailer to the commit")
	return 1
}

func removalReason(dir, ref, flagReason string) string {
	if flagReason != "" {
		return flagReason
	}
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%B").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Allow-Removals:"); ok {
			if reason := strings.TrimSpace(rest); reason != "" {
				return reason
			}
		}
	}
	return ""
}

func gitShow(dir, ref, rel string) ([]byte, bool, error) {
	out, err := exec.Command("git", "-C", dir, "show", ref+":"+rel).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return out, true, nil
}

func runActualsStatus(args []string) int {
	dir := "data"
	if len(args) > 0 {
		dir = args[0]
	}
	months, err := actualsMonths(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl actuals status:", err)
		return 1
	}
	if len(months) == 0 {
		fmt.Println("no months reconciled yet")
		return 0
	}
	fmt.Printf("%-9s %6s %9s %9s %9s  %s\n", "MONTH", "TX", "ACTUAL", "IGNORED", "UNTRACKED", "COVERAGE")
	for _, m := range months {
		af, err := readActuals(m.path)
		if err != nil {
			fmt.Printf("%-9s %s\n", m.key, err)
			continue
		}
		cents, ignored, untracked := 0, 0, 0
		for _, tx := range af.Transactions {
			parts := actualsdata.PartsOf(tx)
			decided := false
			hasUntracked := false
			for _, p := range parts {
				switch {
				case p.Category != "":
					cents += int(p.Amount*100 + 0.5)
					decided = true
				case p.Untracked != "":
					hasUntracked = true
					decided = true
				}
			}
			if hasUntracked {
				untracked++
			}
			if !decided {
				ignored++
			}
		}
		var ranges []string
		for _, c := range af.Coverage {
			ranges = append(ranges, fmt.Sprintf("%s %s..%s", c.Account, c.From, c.To))
		}
		fmt.Printf("%-9s %6d %9.2f %9d %9d  %s\n", m.key, len(af.Transactions), float64(cents)/100, ignored, untracked, strings.Join(ranges, "; "))
	}
	return 0
}

func runActualsCategories(args []string) int {
	dir := "data"
	if len(args) > 0 {
		dir = args[0]
	}
	b, err := os.ReadFile(filepath.Join(dir, "budget.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl actuals categories:", err)
		return 1
	}
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(b, &bf); err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl actuals categories:", err)
		return 1
	}
	fmt.Printf("%-40s %-9s %-24s %s\n", "ID", "KIND", "CATEGORY", "GROUP")
	for _, g := range bf.Groups {
		for _, c := range g.Categories {
			fmt.Printf("%-40s %-9s %-24s %s\n", c.Id, g.Kind, c.Name, g.Name)
		}
	}
	return 0
}

type monthFile struct {
	key  string
	path string
}

func actualsMonths(dir string) ([]monthFile, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "actuals"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []monthFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, monthFile{key: actualsdata.MonthKeyOf(e.Name()), path: filepath.Join(dir, "actuals", e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

func readActuals(path string) (actualsdata.ActualsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return actualsdata.ActualsFile{}, err
	}
	var af actualsdata.ActualsFile
	if err := json.Unmarshal(b, &af); err != nil {
		return actualsdata.ActualsFile{}, err
	}
	return af, nil
}

func budgetCategoryIDs(dir string) map[string]bool {
	b, err := os.ReadFile(filepath.Join(dir, "budget.json"))
	if err != nil {
		return nil
	}
	var bf budgetdata.BudgetFile
	if json.Unmarshal(b, &bf) != nil {
		return nil
	}
	out := map[string]bool{}
	for _, g := range bf.Groups {
		for _, c := range g.Categories {
			out[c.Id] = true
		}
	}
	return out
}
