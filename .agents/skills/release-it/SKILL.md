---
name: release-it
description: >
  Propose and cut a new version release: analyze Conventional Commits since
  the last tag, propose a next version, get the user's explicit go-ahead,
  then verify/tag/push. Use when the user asks to "release", "cut a
  release", "make a new version", "tag a release", or similar — never tag
  or push a release without running this skill's confirmation step first.
metadata:
  short-description: "Analyze commits, propose a version, tag on confirmation"
---

# release-it

Replaces GitHub Actions release-please: this repo tags releases from a
human-confirmed proposal instead of automated PRs. Releasing is a visible,
hard-to-reverse action (a pushed tag triggers `.github/workflows/release.yml`,
which publishes a real Docker image to GHCR) — never skip the confirmation
step in Phase 3 below, and never tag/push without it.

## Phase 1 — Gather state

1. `git status --short` — the working tree should be clean (everything
   already shipped via the `ship-it` skill). If not, stop and tell the user
   what's uncommitted; don't release with dirty state.
2. `git fetch origin --tags` then check whether the local branch is ahead of
   `origin/main` (`git log origin/main..HEAD --oneline`). If there are
   unpushed commits, they need to reach `origin/main` before or alongside
   the tag — flag this to the user as part of the proposal in Phase 2, don't
   push it silently.
3. Find the last release tag: `git describe --tags --abbrev=0 2>/dev/null`.
   - **No tags exist yet** (this repo's first release): the commit range is
     the entire history (`git log --oneline`), and the proposed version is
     always **v0.1.0** — a first release doesn't get computed from commit
     types, it's just the starting point.
   - **A tag exists**: the commit range is `<last-tag>..HEAD`.

## Phase 2 — Analyze commits and propose a version

1. `git log <range> --pretty=format:'%s'` and categorize each subject line
   by [Conventional Commits](https://www.conventionalcommits.org/) prefix:
   - `feat:` / `feat(scope):` → **feature** (minor bump)
   - `fix:` / `fix(scope):` → **fix** (patch bump)
   - Any type with `!` before the colon (`feat!:`, `fix!:`, ...), or a
     commit whose full body (`git log <range> --pretty=format:'%B'`)
     contains a `BREAKING CHANGE:` footer → **breaking** (major bump)
   - Anything else (`chore:`, `docs:`, `refactor:`, `test:`, `ci:`, or no
     recognized prefix at all) → **other** (no bump signal by itself)
2. Skip this whole categorization step for a first release (no tags yet) —
   go straight to proposing v0.1.0 per Phase 1.
3. For a subsequent release, compute the proposed next version from the last
   tag using standard semver rules: any breaking commit → bump major (and
   reset minor/patch to 0); else any feature commit → bump minor (reset
   patch to 0); else (only fixes/other) → bump patch. Pre-1.0 (`v0.x.y`)
   stays on the same minor-bump-for-breaking-too convention unless the user
   says otherwise when asked — don't silently jump to v1.0.0 without asking.
4. Build a short categorized summary (feature/fix/breaking/other, one line
   per commit, short hash + subject) — this becomes both what you show the
   user in Phase 3 and the basis for the tag message / GitHub Release notes
   in Phase 4.

## Phase 3 — Confirm with the user (never skip this)

Present the proposal plainly: the last release (or "no releases yet"), the
categorized commit list, the proposed next version and why, and whether
`origin/main` needs a push first. Then use AskUserQuestion (or, if the
answer is obvious from a direct instruction already in the conversation,
a plain confirmation) to get an explicit go-ahead — offer the proposed
version as the recommended option, but let the user pick a different one or
cancel. Do not proceed past this point without an explicit yes.

## Phase 4 — Verify, tag, push

Only after Phase 3's confirmation:

1. Run the full check: `gofmt -l .` (clean), `go generate ./...`,
   `go vet ./...`, `go build ./...`, `go test ./...`. Do not run
   `docker build` — the release workflow builds the image itself, and step 6
   below watches that build rather than duplicating it here. Stop and fix (or
   report and stop) on any failure — never tag a release that doesn't pass
   its own checks.
2. If `origin/main` needs a push (per Phase 1), push it now:
   `git push origin main`.
3. Create an annotated tag with the categorized summary as its message:
   `git tag -a vX.Y.Z -m "<summary>"`.
4. Push the tag: `git push origin vX.Y.Z`. This is what triggers
   `.github/workflows/release.yml`'s GHCR image publish — nothing else does.
5. Create the GitHub Release from the same notes:
   `gh release create vX.Y.Z --title vX.Y.Z --notes "<summary>"`.
6. **Track the image build to completion.** The tag push is what triggers
   `.github/workflows/release.yml`'s GHCR publish, and that build is the only
   place the image is checked at all — a release whose image never built is a
   release that cannot be deployed. Find the run with `gh run list --limit 3`
   and wait on it with `gh run watch <id> --exit-status`; a push to `main` in
   the same breath starts a second `Build` run worth watching too. Run these
   in the background rather than blocking, and report each conclusion.
7. Report back: the release URL, the tag pushed, and whether the image build
   went green. If it failed, say so plainly and treat it as a broken release
   to fix, not a footnote — do not describe the release as done.
