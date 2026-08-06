// Command invoicectl drives the invoicing pipeline (creating, validating,
// rendering, and indexing invoices — see ARCHITECTURE.md for the full
// design) plus validate, which checks every data file PocketCFO reads at
// runtime (invoicing and finance alike — see validate.go), since this is
// the only CLI binary in the repo and a real data checkout (the private
// pocket-cfo-data repo) needs one place to validate all of it in CI.
package main

import (
	"fmt"
	"os"
)

// plannedCommands documents commands ARCHITECTURE.md describes but that are
// not implemented yet. They are listed here so `help` stays accurate as the
// CLI grows.
var plannedCommands = []struct {
	name string
	desc string
}{
	{"new", "stub a new invoice from a recipient"},
	{"duplicate", "duplicate an existing invoice"},
	{"index", "build build/index.json and build/stats.json"},
}

var commands = map[string]func([]string) int{
	"help":      runHelp,
	"render":    runRender,
	"translate": runTranslate,
	"delete":    runDelete,
	"prune":     runPrune,
	"validate":  runValidate,
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return runHelp(args)
	}

	name := args[0]
	if name == "-h" || name == "--help" {
		return runHelp(args)
	}

	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "invoicectl: unknown command %q\n", name)
		fmt.Fprintln(os.Stderr, "Run 'invoicectl help' for usage.")
		return 1
	}
	return cmd(args[1:])
}

func runHelp(_ []string) int {
	fmt.Println("invoicectl — invoicing pipeline CLI")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  invoicectl <command> [arguments]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  help        show this help message")
	fmt.Println("  render      render invoice PDFs (see 'render -h')")
	fmt.Println("  translate   fill missing Bulgarian text on draft invoices via DeepL")
	fmt.Println("  delete      delete a draft invoice and its build artifacts (see 'delete -h')")
	fmt.Println("  prune       remove build/*-DRAFT.pdf relics whose JSON no longer exists (see 'prune -h')")
	fmt.Println("  validate    validate a data checkout's recipients/invoices/users.json/budget.json")
	fmt.Println("              against their schemas and business rules ('validate [dir]', default data)")
	fmt.Println()
	fmt.Println("Planned (not yet implemented):")
	for _, c := range plannedCommands {
		fmt.Printf("  %-10s  %s\n", c.name, c.desc)
	}
	return 0
}
