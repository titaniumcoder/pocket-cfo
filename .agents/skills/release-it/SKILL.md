---
name: release-it
description: >
  Propose and cut a new version release: analyze Conventional Commits since
  the last tag, propose a next version, get the user's explicit go-ahead,
  then verify/tag/push. Use when the user asks to "release", "cut a
  release", "make a new version", "tag a release", or similar, and for
  pre-releases: "cut a pre-release", "release candidate", "an rc",
  "promote the rc", "cut the final" — never tag or push a release without
  running this skill's confirmation step first.
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
3. Find two anchors. The **last stable tag** is
   `git describe --tags --abbrev=0 --exclude='*-*' 2>/dev/null` — a tag with
   a `-` in it is a pre-release (`v2.0.0-rc.1`) and never counts as a release
   to compute from. The **last tag of any kind** is
   `git describe --tags --abbrev=0 2>/dev/null`; it differs from the stable
   one only while a pre-release series is open.
   - **No tags exist yet** (this repo's first release): the commit range is
     the entire history (`git log --oneline`), and the proposed version is
     always **v0.1.0** — a first release doesn't get computed from commit
     types, it's just the starting point.
   - **A tag exists**: the commit range is `<last-stable-tag>..HEAD` — always
     since the last *stable* one, so a second release candidate and the
     final release both cover everything since the last real release rather
     than only what changed since the previous candidate.
4. Decide the mode from what the user asked for: a **release** (the
   default), a **pre-release** ("rc", "release candidate", "pre-release",
   "let me try it first"), or a **promotion** ("cut the final", "promote the
   rc", "release 2.0.0 now" while `vX.Y.Z-rc.*` tags exist). List the
   candidates already cut for the version in question with
   `git tag --list 'vX.Y.Z-rc.*'`.

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
   per commit, short hash + subject) — this is what you show the user in
   Phase 3.
5. **Draft the changelog entry from the commits — always, every release.**
   Walk every commit in the range (`git log <range> --reverse --pretty=format:'%h %s%n%b'`)
   and write one line per user-visible change into the
   [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) subsections:
   - `feat` → **Added** (or **Changed** when it alters existing behaviour —
     "no longer", "instead of", "moves", renames)
   - `fix` → **Fixed**
   - a removal of a feature, option, route or file → **Removed**
   - `!` or a `BREAKING CHANGE:` footer → the line starts with **Breaking:**
     in whichever subsection it belongs to
   - `docs`, `test`, `ci`, `chore`, `refactor` and merge commits → no line,
     unless the change is something an operator notices (a new env var, a
     changed default, a dropped file in the image)
   Write each line for the reader of the release notes — what changed for
   them, not the commit subject verbatim — and drop the scope prefix. Then
   merge that draft with whatever `## [Unreleased]` in `CHANGELOG.md` already
   holds (ship-it adds lines there as it goes): keep one line per change,
   prefer the better-worded of two duplicates, and never lose a line that is
   only in one of the two. The result is the version's changelog section and
   the GitHub Release notes in Phase 4; show it in Phase 3 beside the
   commit list.
6. From 1.0.0 on the project follows strict semantic versioning: a breaking
   commit bumps the major version, no exceptions and no asking. The one
   thing the commits cannot decide is a *deliberate* major: when the user
   names the version themselves ("this is 2.0"), accept it — say in the
   proposal what the commits alone would have given, and go with the
   user's number.
7. **Pre-release mode**: the proposal is `vX.Y.Z-rc.N`, where `X.Y.Z` is the
   version computed above and `N` is one more than the highest existing
   `vX.Y.Z-rc.*` tag, or `rc.1` when there is none. A breaking commit that
   arrives in the middle of a series changes `X.Y.Z`, and the new version
   starts its own series at `rc.1`; the old candidates simply stay behind.
   The changelog section drafted in step 5 becomes `## [X.Y.Z-rc.N] - date`
   — a candidate has its own section, so what each one shipped stays
   readable while the series is open.
8. **Promotion**: the proposal is the plain `vX.Y.Z` the candidates carried,
   and it may well point at the very commit the last candidate tagged —
   that is the normal case, not a problem. The changelog section is the
   step 5 draft merged with **every** `## [X.Y.Z-rc.*]` section already in
   the file, by the same rules (one line per change, the better-worded of
   two duplicates, nothing lost), into a single `## [X.Y.Z] - date`; the
   candidate sections are removed, and the merged section opens with one
   line naming them ("Previewed as 2.0.0-rc.1 and 2.0.0-rc.2.") so they stay
   findable. Nobody stays on a release candidate, so nobody needs its
   section once the release exists.

## Phase 3 — Confirm with the user (never skip this)

Present the proposal plainly: the last release (or "no releases yet"), the
categorized commit list, the drafted changelog section, the proposed next
version and why, and whether `origin/main` needs a push first. Say which
mode it is: a pre-release publishes the image under its own tag and `:next`,
never `latest`, is marked pre-release on GitHub and does not notify the data
repo; a promotion says which candidate sections fold into the final entry.
Then use AskUserQuestion (or, if the
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
2. Update `CHANGELOG.md` ([Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/)):
   replace the body of `## [Unreleased]` with the section drafted in Phase 2
   step 5, rename that heading to `## [X.Y.Z] - YYYY-MM-DD`, open a fresh
   empty `## [Unreleased]` above it, and add the two link references at the
   bottom (`[Unreleased]: .../compare/vX.Y.Z...HEAD`,
   `[X.Y.Z]: .../compare/<last>...vX.Y.Z`). A release never ships with an
   empty section: if the range has only commits that earn no line, the
   section says so in one sentence (as 0.3.1 and 0.5.1 do). Commit that as
   `chore(release): vX.Y.Z` and push it with `origin/main` in the next step.
   For a pre-release the heading is `## [X.Y.Z-rc.N] - YYYY-MM-DD` and its
   compare link runs from the last tag of any kind (the previous candidate,
   or the last stable release for `rc.1`). For a promotion the candidate
   sections and their link lines go, per Phase 2 step 8, and `[X.Y.Z]`'s
   compare link runs from the last stable tag.
3. If `origin/main` needs a push (per Phase 1), push it now:
   `git push origin main`.
4. Create an annotated tag with the categorized summary as its message:
   `git tag -a vX.Y.Z -m "<summary>"`.
5. Push the tag: `git push origin vX.Y.Z`. This is what triggers
   `.github/workflows/release.yml`'s GHCR image publish — nothing else does.
6. Create the GitHub Release with the new changelog section as its notes:
   `gh release create vX.Y.Z --title vX.Y.Z --notes "<changelog section>"`.
   A pre-release adds `--prerelease`, which also keeps it from becoming the
   repository's "latest release".
7. **Track the image build to completion.** The tag push is what triggers
   `.github/workflows/release.yml`'s GHCR publish, and that build is the only
   place the image is checked at all — a release whose image never built is a
   release that cannot be deployed. Find the run with `gh run list --limit 3`
   and wait on it with `gh run watch <id> --exit-status`; a push to `main` in
   the same breath starts a second `Build` run worth watching too. Run these
   in the background rather than blocking, and report each conclusion.
8. Report back: the release URL, the tag pushed, and whether the image build
   went green. If it failed, say so plainly and treat it as a broken release
   to fix, not a footnote — do not describe the release as done. For a
   pre-release, also confirm what the workflow promised: `:next` moved to
   the new image, `latest` did not
   (`gh api /users/titaniumcoder/packages/container/pocket-cfo/versions --jq '.[].metadata.container.tags'`),
   and the "Notify pocket-cfo-data" job was skipped.
