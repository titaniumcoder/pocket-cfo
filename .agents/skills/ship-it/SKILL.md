---
name: ship-it
description: >
  Run this repo's per-step commit ritual and create exactly one commit for the
  completed step. Use whenever a subtask (in the PocketCFO merge plan, or any
  later change to this repo) is functionally done and ready to be committed —
  never commit by hand instead of via this skill. Triggers on "ship it",
  "ship this", "commit this step", or finishing a plan subtask.
metadata:
  short-description: "gofmt/vet/build/test/manual-test, then one commit"
---

# ship-it

Runs this repo's mandatory pre-commit ritual for one completed step, then
creates exactly one commit for it. Do not skip a step and do not commit if any
step fails — fix the failure (or report it and stop) instead.

## Steps, in order

1. **`gofmt -l .`** — must print nothing. If it prints files, run `gofmt -w .`
   on them and re-check.
2. **`go generate ./...`** — regenerate schema-derived Go types if any
   `schemas/*.json` or other schema-owning JSON changed in this step. Skip
   only if nothing schema-related changed.
3. **`go vet ./...`** — must be clean.
4. **`go build ./...`** — must succeed.
5. **`go test ./...`** — must pass.
6. **Manual test** — actually run the changed behavior (start the server and
   hit the route, or run the CLI command) rather than trusting the automated
   checks alone. Describe what was exercised and what was observed.
7. **Changelog** — if the step changes anything an operator or user can
   notice (behaviour, configuration, routes, output, data formats), add one
   line for it under `## [Unreleased]` in `CHANGELOG.md`, in the right
   Added / Changed / Deprecated / Removed / Fixed / Security subsection,
   written for the reader of the release notes rather than as the commit
   subject. Pure refactors, tests, CI and docs need no line.
8. **Commit** — stage only the files belonging to this one step (never a
   blanket `git add -A`), write a commit message describing this step alone,
   and create the commit. Do not push. Do not batch multiple unrelated steps
   into one commit, and do not amend a previous commit to fold in more work.

If any of steps 1-7 fails, stop and fix the root cause before retrying from
that step — never commit past a failing check, and never use `--no-verify` to
route around a failing hook.

## The image is CI's job, not this skill's

Do not run `docker build` here. The `Build` workflow builds the image on every
push to `main` and the `Release` workflow builds and publishes it on a tag, so
building it locally duplicates a check that runs anyway — and it is the check
most likely to be unavailable (no daemon, no `docker` group), which turns a
green step into a paragraph of hedging about why it was skipped.

Once the commit is pushed, track the run instead of building anything:
`gh run list --limit 3` to find it, then `gh run watch <id> --exit-status`.
Report whether it went green. If it fails, that is a real failure to fix, not
a footnote — the image is what actually gets deployed.
