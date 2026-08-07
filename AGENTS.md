# AGENTS.md

`ARCHITECTURE.md` is the canonical design doc — read it first. This file only records
conventions established during scaffolding that aren't obvious from the code itself.

## Conventions

- **Stdlib first.** Only reach for a third-party dependency when there's a genuinely
  strong reason. The CLI uses `flag`/`os.Args`, not `cobra`. The web app uses
  `net/http` + `html/template`, not a router library. The one named exception:
  `go-jsonschema`, used as a dev-time `tool` dependency (Go 1.24+ `tool` directive) to
  generate Go structs from `schemas/*.json` — never imported by runtime code. Digital
  signing (`internal/sign`, later) is expected to be a second such exception.
- **Schema documents are `go:embed`ed**, never read from disk at runtime — every file
  under `schemas/` (`issuer.json`, `recipient.json`, `invoice.json`, `notes.json`,
  `users.json`) plus `internal/finance/data/budget.schema.json`. This does not apply to
  `data/**` (recipients, invoices, `users.json`, `budget.json`), which is hand-edited and
  read fresh from disk on every request/check (see `DATA_DIR`/`BUILD_DIR` in
  README.md; reading it via the GitHub Contents API instead is a documented
  future direction, not current behavior — see ARCHITECTURE.md §8), nor to
  `templates`/`static`, which are read from disk
  since `embed` can't cross outside a package's own directory tree anyway. Every schema
  gets generated Go types via `go-jsonschema` (`internal/schema/<model>/`,
  `internal/finance/budgetdata/`) — never a hand-written struct for a *data* shape, even
  a small one; the one deliberate exception is `config.json` (pure deployment tunables,
  not data — see `internal/finance/config`).
- **`internal/schema/<model>/` holds generated code only.** `go generate ./...`
  (or `make generate`) regenerates it from `schemas/*.json` via `go-jsonschema` — never
  hand-edit files there.
- **Build/commit ritual.** See "Agentic execution workflow" below.

## Releases

Commit messages on `main` must follow [Conventional Commits](https://www.conventionalcommits.org/)
— `feat: ...` / `fix: ...` / `feat!: ...` (or a `BREAKING CHANGE:` footer) / etc. — because
the **`release-it`** skill (`.agents/skills/release-it/SKILL.md`, symlinked from
`.claude/skills/` for Claude Code's discovery) reads them to propose the
next version. Releases are cut locally, on request, not automated by GitHub Actions:
`release-it` analyzes commits since the last tag, categorizes them (feature/fix/breaking),
proposes a version (semver-derived, or `v0.1.0` for the first release), and — only after
the user explicitly confirms the proposal — runs the full verify checklist, tags, and
pushes. Pushing the tag is what triggers `.github/workflows/release.yml`'s two publish
jobs; nothing else does, and day-to-day commits to `main` never publish anything by
themselves. One Docker image (`ghcr.io/titaniumcoder/pocket-cfo`), containing **both**
executables (`pocketcfo`, the web server; `invoicectl`, the CLI), ships both the finance
tracker and invoicing, tagged with the release version and `latest`. The same two
executables are also cross-compiled (no CGO dependencies anywhere in the module, so
one Linux runner builds every target) and attached directly to the GitHub Release as
downloadable archives — `pocketcfo_<version>_<os>_<arch>.{tar.gz,zip}` for
linux/{amd64,arm64}, darwin/{amd64,arm64}, and windows/amd64.

A subtask's own commit message (see the ritual below) doesn't need to be a release-worthy
`feat`/`fix` itself — only commits that land on `main` do, and even those, `release-it`
tolerates a non-conventional one by just filing it under "other" (no version-bump signal,
not a hard failure) — but writing them correctly from the start avoids surprises at
release time.

## Agentic execution workflow

- **Plan in small, complete subtasks.** Break any non-trivial task into a sequence of
  small subtasks, each one a complete, independently-verifiable unit of work — not a
  batch of unrelated changes.
- **Per-subtask ritual.** Run the `ship-it` skill (`.agents/skills/ship-it/SKILL.md`) to
  close out a subtask — it runs the full checklist (fmt/generate/vet/build/test/
  manual-test/docker-build) and creates exactly one commit for it. Do not push yet.
- **End of plan.** Once every subtask in the plan is done, push, then watch the GitHub
  Actions run for that push to confirm it's green before considering the plan finished.

See README.md's "Building and running" section for local dev commands — this file
doesn't duplicate them.
