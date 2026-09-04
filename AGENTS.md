# AGENTS.md

`ARCHITECTURE.md` is the canonical design doc for the **invoicing** side — read it first
when working there. The finance tracker's design lives inline in `internal/finance`, and
the agent-facing write surface in `docs/HERMES.md`. This file only records conventions
that aren't obvious from the code itself.

## Conventions

- **Stdlib first.** Only reach for a third-party dependency when there's a genuinely
  strong reason. The CLI uses `flag`/`os.Args`, not `cobra`. The web app uses
  `net/http` + `html/template`, not a router library. The one named exception:
  `go-jsonschema`, used as a dev-time `tool` dependency (Go 1.24+ `tool` directive) to
  generate Go structs from `schemas/*.json` — never imported by runtime code. The second
  is `github.com/modelcontextprotocol/go-sdk` for the MCP server at `/mcp`: a moving
  external wire protocol with strict framing, where being subtly wrong presents as
  *silence* — the agent sees no tools at the moment you need it — rather than as an
  error. It is kept reversible by three rules, enforced by tests: it is imported by
  exactly one file (`internal/api/mcp.go`), no SDK type crosses into the service layer,
  and the `/mcp` conformance tests drive raw HTTP with hand-written JSON-RPC rather than
  the SDK's own client, so they would equally validate a hand-rolled replacement. The
  tools themselves — names, descriptions, argument structs and their JSON schemas — live
  SDK-free in `internal/api/tools.go`, which uses `github.com/google/jsonschema-go`
  (the schema library the MCP SDK itself is built on) to derive a schema from each
  argument struct, so the same catalog serves `/mcp` and the in-app chat and neither
  adapter can drift from the other. The third dependency linked into a binary is
  `github.com/openai/openai-go/v3`, for the chat's calls to an OpenAI-compatible endpoint
  (ARCHITECTURE.md §12): a wire protocol with the same failure mode as MCP's, held to the
  same three rules — imported by exactly one file (`internal/chat/openai.go`), no SDK
  type past it, and tests that drive a stub endpoint with hand-written JSON.
- **Schema documents are `go:embed`ed**, never read from disk at runtime — every file
  under `schemas/` plus all three under `internal/finance/data/` (`budget`, `accounts`,
  `actuals`). This does not apply to `data/**`, which is hand-edited and read fresh from
  disk on every request/check (see `DATA_DIR`/`BUILD_DIR` in README.md), nor to
  `templates`/`static`, which are read from disk since `embed` can't cross outside a
  package's own directory tree anyway. Every schema gets generated Go types via
  `go-jsonschema` (`internal/schema/<model>/`, `internal/finance/{budget,accounts,actuals}data/`)
  — never a hand-written struct for a *data* shape, even a small one; the one deliberate
  exception is `config.json` (pure deployment tunables, not data — see
  `internal/finance/config`).
- **Writes go through git, never to disk.** `DATA_DIR` is an ephemeral mounted checkout:
  a write landing there is lost on restart and diverges from the repo. Everything the
  Hermes API accepts is committed through the GitHub Contents API instead, which is what
  makes an agent-facing write surface tolerable — it isn't trusted, it's audited. See
  `docs/HERMES.md`.
- **The code explains itself; comments do not.** Go code here carries no comments.
  When a block needs explaining, that is the signal to extract it into a function
  whose name is the explanation — `refuseDestruction`, `privateExpenseStartMonth`,
  `describeSchedule` — or to name the constant, or to split the function until each
  piece states its own intent. A comment is the option of last resort, and there
  isn't one.
  - **Where the *why* goes instead.** A fact from outside the code — a tax rule, a
    vendor's behaviour, a regulator's requirement, a domain rule like the two-month
    funding shift — cannot be carried by an identifier, so it belongs in
    `ARCHITECTURE.md` (§10 and §11 for the finance and agent halves) or in
    `docs/`. Write it there, once, and let the code be the mechanism.
  - **What survives in a `.go` file.** Compiler directives only: `//go:generate`,
    `//go:embed`, `//go:build`. Those are syntax, not commentary.
  - **Tests are the exception**, and are where an intent that resists naming should
    land. A test name is a sentence, and a test that fails is a comment nobody can
    let rot — prefer `TestUntrackedCashReachesNoFinanceFigure` over a paragraph
    above the field it protects.
- **Generated types are build output, not source, and are not checked in.**
  `go generate ./...` (or `make generate`) regenerates them from `schemas/*.json` and
  `internal/finance/data/*.schema.json` via `go-jsonschema`; every one is gitignored, so
  **a fresh clone does not compile until you run it.** CI, the Dockerfile and the release
  workflow each run it as their first step. The generated file in
  `internal/schema/<model>/` is the one named after its package (`invoice/invoice.go`) —
  never hand-edit it. Anything else in that directory (`invoice/helpers.go` and the
  tests) is ordinary hand-written code and is checked in normally.
- **Build/commit ritual.** See "Agentic execution workflow" below.

## Releases

`CHANGELOG.md` follows [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/):
`ship-it` adds a line under `[Unreleased]` for every user-visible step, and `release-it`
turns that section into the version's entry. From 1.0.0 the versions are strict semver.

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
executables (`pocketcfo`, the web server; `pocket-cfo-ctl`, the CLI), ships both the finance
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
  close out a subtask — it runs the full checklist (fmt/generate/vet/build/test plus a
  real manual exercise of the change) and creates exactly one commit for it. It
  deliberately does *not* build the image: CI does that on every push, and duplicating
  the one check most likely to be unavailable locally turns a green step into a paragraph
  of hedging. Do not push yet.
- **End of plan.** Once every subtask in the plan is done, push, then watch the GitHub
  Actions run for that push to confirm it's green before considering the plan finished.

See README.md's "Building and running" section for local dev commands — this file
doesn't duplicate them.
