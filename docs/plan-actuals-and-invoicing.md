# Actuals tracking, and splitting payment out of the invoice

Three independent workstreams.

| | Status |
|---|---|
| **Part A** — weekly bank-statement reconciliation: real spending recorded per month, shown beside the plan, drillable by an admin. Hermes keeps the parsing and matching; this app gets a stable read/write API and an MCP server in front of it. | **not started** |
| **Part B** — move `paid` out of the invoice JSON into `data/paid-invoices.json`, drop annulment until there's a real one, and stop drafts and paid invoices from being flagged stale. | **not started** — do this next, it's small |
| **Part C** — stop Toggl timeouts from blanking and blocking the dashboard. | **done**, merged in #1 |

Every decision recorded below was made with the user in the planning conversation; the
options are settled, not open. Where the plan says "flagged" or "one interpretation to
flag", that caveat is still live and worth re-reading before building that piece.

Part C shipped as six commits on `toggl-resilience`. Two things it left behind that Part A
and B inherit: `docker build` was never run (no Docker daemon on the Windows dev machine),
and `go test -race` was never run (needs cgo, no C compiler there). Both work on Linux and
are worth running against the tracker package before building on top of it.

---

# Part A — Actuals tracking

## Context

`data/budget.json` is a **plan**: every category is a name and an amount that recurs
monthly. Nothing anywhere records what was *actually* spent. `internal/finance/tracker/budget.go`
says so in its own doc comment: *"not envelope budgeting and not actual tracking — there's
no logged spending"*.

The goal is a weekly reconciliation loop. Once a week the user hands their bank statements
to a **Hermes agent**; Hermes reads them, compares each line against the budget for that
period, asks the user about anything it can't place, and writes the result as JSON. The
dashboard then shows planned *and* actual side by side, and an admin can click a category
to see the individual transactions behind its number.

Constraints agreed up front:

- **Display only.** Actuals never change the arithmetic. `Total private expenses`,
  `Balance`, `Available to spend` and the account roll-forward all stay planned-based
  exactly as today. Nothing existing can regress.
- **Private + company expenses.** Both group kinds get actuals. Income and inter-account
  transfers are not tracked as figures — they are recorded as explicitly ignored lines
  (see below).
- **Nothing shown when there's nothing to show.** A period with no imported statement
  renders byte-identically to today.
- **Stable category ids.** Budget categories get an `id`, so a rename doesn't orphan
  history.
- **Hermes keeps the judgement, this app keeps the truth.** Parsing statements, matching
  lines to categories and asking on Signal stay in Hermes — reproducing that here would be
  a lot of effort for something that already works. This app exposes a stable read/write
  API (REST + MCP) and owns the schema, the validation and the commit. See §7.
- **The committed JSON is the source of truth, not Hermes' memory.** Hermes' recollection
  of recurring merchants is a *prior*, never an authority; where the two disagree, the file
  wins. No rules file lives in this repo — instead the API makes history searchable
  (`search_transactions`), so matching is grounded in what was actually committed rather
  than in a lookup table that can quietly drift out of sync with it.
- **Nothing may silently vanish.** A write that removes or mutates an already-recorded
  transaction is rejected unless explicitly overridden. See §8.
- **Drill-down is a separate admin-only page.** Transaction descriptions never reach a
  non-admin session's HTML.

### One interpretation to flag

You chose *"Hermes must always ask"* — the file may never contain an undecided line. A
salary credit or an own-account transfer, though, isn't a budget expense and has no
category. If such lines simply aren't recorded, the file stops reconciling to the statement
and there's no way to tell "decided to ignore" from "never seen".

Resolution baked into the schema: **every transaction carries exactly one of `category` or
`ignored`**, where `ignored` is a non-empty reason string ("salary", "transfer to savings").
No line is ever undecided, the file accounts for every statement row, and the *file* — not
Hermes' recollection — is what says a line was deliberately skipped. If you'd rather ignored
lines stay out entirely, say so and I'll drop `ignored`.

---

## 1. Category ids in `budget.json`

**`internal/finance/data/budget.schema.json`** — add to `$defs/category`:

```json
"id": {
  "type": "string",
  "pattern": "^[a-z0-9]+([-.][a-z0-9]+)*$",
  "description": "Stable identifier a transaction in data/actuals/ points at. Unique across the whole file. Survives renaming name/group."
}
```

Add `"id"` to the category `required` list. It becomes mandatory — a half-migrated file
where some categories can be reconciled and some can't is worse than a single cutover.

**`internal/finance/budgetdata/validate.go`** — extend `ValidateBudget`: id present,
matches the pattern, unique across *all* groups (not just within one, unlike `name`).

**`cmd/pocket-cfo-ctl/budget.go`** (new) — `pocket-cfo-ctl budget add-ids [dataDir]`:
one-time migration that reads `budget.json`, fills a slugified `<group>.<category>` id into
every category missing one, and rewrites the file. Follows the existing in-place rewrite
pattern in `cmd/pocket-cfo-ctl/translate.go:97-103` (`json.MarshalIndent` + `os.WriteFile`).
Run it once against the private data checkout; run it again any time a category is added
by hand.

Update the sample `data/budget.json` with ids.

Regenerate: `make generate` (`internal/finance/budgetdata/generated.go` is gitignored and
must be regenerated before the tree compiles).

## 2. The actuals file

One file per month, filename = the month it covers — same "filename is the key" convention
as `data/invoices/INV-….json` and `data/recipients/0001.json`.

**`data/actuals/2026-08.json`**

```json
{
  "$schema": "../../internal/finance/data/actuals.schema.json",
  "month": "2026-08",
  "coverage": [
    { "account": "Private Checking", "from": "2026-08-01", "to": "2026-08-09", "imported_at": "2026-08-10" }
  ],
  "transactions": [
    { "id": "a3f21c9b", "date": "2026-08-03", "description": "LIDL SOFIA 4412",
      "amount": 42.18, "account": "Private Checking", "category": "private-expenses.groceries" },
    { "id": "7d10be44", "date": "2026-08-05", "description": "SEPA CREDIT SALARY",
      "amount": 2400, "account": "Private Checking", "ignored": "salary" }
  ]
}
```

Field rules, enforced by a hand-written `ValidateActuals` (JSON Schema is only used for
codegen in this repo — see AGENTS.md — so anything beyond structure has to be coded):

| Rule | Why |
|---|---|
| `month` matches the filename | catches a mis-saved import |
| `id` unique within the file | re-uploading the same statement is idempotent. Hermes derives it as a short hash of `account+date+amount+description`, `-2` suffix on collision |
| exactly one of `category` / `ignored` | the "always ask" rule — no undecided line survives validation |
| `category` resolves to a budget category id | a renamed/deleted category fails loudly instead of silently dropping money |
| `amount` euros, decimals allowed, **positive = money out** | matches `budget.json`'s convention; a refund is a negative amount against the same category |
| `date` inside `month`; every `coverage` range inside `month` | a statement spanning a month boundary splits into two files |
| `coverage` non-empty | "nothing imported" is the absent file, not an empty one |

`coverage` is what stops a mid-month import from reading as a spectacular underspend: the
UI reports what has actually been read rather than implying the month is complete.

**Schema + generated types:** `internal/finance/data/actuals.schema.json` (embedded via the
existing `internal/finance/data/data.go`), generated into `internal/finance/actualsdata/`
with a `gen.go` copied from `internal/finance/budgetdata/gen.go`. Add the new generated file
to `.gitignore` alongside the other two, and to the `Makefile` generate target.

## 3. Loader — `internal/finance/tracker/actuals.go`

Mirror `accounts.go` exactly: an `fs.FS`, a mutex, cache-until-evicted, **a missing file is
not an error** (an unreconciled month), nil receiver behaves as "layer not configured".
Cache is a `map[string]*actualsResult` keyed `"2026-08"` since there's a file per month.

```go
type Actuals struct { FS fs.FS; mu sync.Mutex; cache map[string]*actualsResult }

type ActualsView struct {
    ByCategory    map[string]int // budget category id -> cents
    TotalCents    int
    CoverageLabel string         // "reconciled through 9 August 2026 · Private Checking"
    Present       bool           // false = no file for this period; render nothing
}

func (a *Actuals) ForMonth(ctx context.Context, year int, month time.Month) (ActualsView, error)
func (a *Actuals) ForYear(ctx context.Context, year int) (ActualsView, error)  // sums the months that exist
func (a *Actuals) TransactionsForMonth(ctx context.Context, year int, month time.Month) (ActualsMonth, error) // detail page
func (a *Actuals) Evict()
```

Euros → cents via the existing `eurToCents` (`budget.go:493`). Ignored lines never enter
`ByCategory` or `TotalCents`.

Wire it in `cmd/pocketcfo/main.go` next to `Budget`/`Accounts`
(`&tracker.Actuals{FS: os.DirFS(budgetDir)}`), add `Actuals *Actuals` to `Tracker`
(`view.go:31`), and evict it from `Tracker.EvictMonth`/`EvictYear` (`view.go:728`, `:743`)
so the existing Reload link picks up a fresh commit.

## 4. Threading actuals into the view

- `CategoryRow` (`budget.go:134`) gains `ActualCents int` and `HasActual bool`.
- `CategoryGroupView` (`budget.go:150`) gains `ActualCents int`.
- `buildBudgetView` (`budget.go:284`) takes an extra `actual func(id string) (int, bool)`;
  nil means the layer is off. It only *fills* the new fields — `SpentCents` and every
  existing sum are untouched, which is what keeps this display-only.
- `Figures` (`view.go:85`) gains `ShowActuals bool`, `ActualsCoverageLabel string`,
  `PrivateActualCents int`, `CompanyActualCents int`, `ActualsErr string`, and
  `SpendingDetailURL string`.
- A new `computeActuals` step in `compute()` (`view.go:289`), placed right before
  `computeBudget` since `computeBudget` needs its lookup. On error it sets `ActualsErr` and
  leaves the layer off — same per-section degradation as every other `*Err` field.

`SpendingDetailURL` is filled by the HTTP layer from the session, **not** by `compute()` —
the identical pattern to `InvoicedRow.URL` / `fillInvoiceLinks` (`cmd/pocketcfo/finance.go:123`).
Left empty for anyone who isn't `s.authorized(sess)`.

## 5. Dashboard rendering — `internal/finance/tracker/render.go`

The category row already has a free column: `{{define "categoryGroups"}}` (`render.go:118`)
puts the planned amount in `.mid` and leaves `.amt` **empty**. Actual goes there. No grid
change, no new column, alignment preserved across the panel's three ledgers.

```
label                       mid (planned)      amt (actual)
  Rent                            900 €             900 €
  Groceries                       400 €             487 €     ← .neg, over plan
  Gym                              40 €                 —     ← not charged yet
Housing ▸          1.045 € actual                  −1.085 €   ← group header
```

- Row: `.amt` shows the actual, colour-coded (below). Empty string when `HasActual` is false
  — nothing invented for a category that hasn't been charged.
- Group header: an extra small `<span class="amt actual">` before the existing planned
  total. Header is flex, not subgrid, so this needs one new CSS rule in `static/app.css`.
- Above the private-groups ledger, a single coverage note reusing the existing
  `.stale-note` class: *"Reconciled through 9 August 2026 · Private Checking. Anything
  spent since isn't in these figures."*
- A one-line column header (`Planned` / `Actual`) above the groups, so the two numbers are
  unambiguous.
- **All of it wrapped in `{{if .ShowActuals}}`.** With no actuals file the page is exactly
  what it is today.
- Company groups render through the same `categoryGroups` template, so they get this for
  free inside the Personal-income cascade.

Year view sums whichever month files exist and labels coverage accordingly.

### Colour by budget status

Decided in Go as an enum on `CategoryRow`, not by comparing numbers in the template, so the
thresholds are testable:

```go
type BudgetStatus string // "" | "under" | "over" | "unbudgeted"
```

| Status | Condition | Class | Colour |
|---|---|---|---|
| `over` | actual > planned | `.amt.over` | `--danger` |
| `under` | actual < planned **and the period is fully covered** | `.amt.under` | `--good` |
| `unbudgeted` | actual > 0, planned == 0 | `.amt.unbudgeted` | `--warn` |
| *(empty)* | anything else | — | default text |

**The coverage condition matters.** Without it, on the 5th of the month every category is
"under budget" and glowing green — which is not information, it's flattery, and then
everything turns red in the last week. So green is withheld until `coverage` shows the month
is fully read (or the month is in the past); red for overspend fires immediately, because
that *is* true the moment it happens. If you'd rather have green from day one, it's one
condition to drop — but I'd try it this way first.

### Phone

At ≤600px `.ledger` collapses to two columns (`app.css:116`) and there is genuinely no room
for label + planned + actual. Reuse the pattern the Income panel already established rather
than inventing one: `.income-panel` hides `.mid` and turns `.amt` into a flex column that
stacks a small secondary figure (`.hrs-m`, `app.css:325-341`).

Same shape here: on phone the budget ledger hides `.mid`, and `.amt` stacks

```
  Groceries                          487 €
                                  of 400 €     ← .plan-m, .8em, opacity .6
```

with the actual as the primary figure (it's the one you came to see) and the plan beneath
it. A new `.plan-m` span, mirroring `.hrs-m` exactly, plus a `.budget-panel` block in the
existing `@media (max-width: 600px)` rule. The `Planned`/`Actual` column header is hidden on
phone — the "of" carries the meaning instead.

The group header is already flex and wraps; the actual total moves under the planned total
rather than beside it.

The §6 detail page tables get `.col-secondary` on the date column so phones show description
+ amount only.

## 6. Detail page — admin only

New route in `cmd/pocketcfo/main.go`, after the existing finance routes:

```go
mux.HandleFunc("GET /{year}/{month}/spending", s.financeSpending)
```

`ServeMux` prefers the more specific pattern, so `/{year}/{month}` is unaffected.

Handler in `cmd/pocketcfo/finance.go`, reusing `parseYearMonth`. Gate: **`s.authorized(sess)`
only** — the same tier as `/info`, so an email-OTP readonly session is refused. Return 404
rather than 403 so the route's existence isn't confirmed.

New template `{{define "spending"}}` in `render.go` (the finance templates all live in that
one string literal) + `tracker.RenderSpending`. Content:

- Header via `{{template "sitehead"}}`, month title, back link to `/{year}/{month}`.
- Coverage block: one row per imported account and range.
- One section per group → per category, `id="cat-<category-id>"` so the dashboard's actual
  amount links straight to it. Each is a `table.data` inside `.table-wrap` (reusing the
  invoicing table primitives): date, description, amount. Footer row: actual subtotal vs
  planned, and the variance.
- A closing "Not budget expenses" section listing the ignored lines with their reason — so
  the page reconciles to the full statement and you can spot a mis-ignored line.

On the dashboard, each category row's actual amount becomes a link to
`{{$.SpendingDetailURL}}#cat-<id>` when `SpendingDetailURL` is non-empty; plain text
otherwise. Same shape as the existing `InvoicedRow.URL` conditional at `render.go:248`.

## 7. The API — `internal/api` + `cmd/pocketcfo/api.go`

**Division of labour.** Hermes keeps the parts that need judgement — reading the statement,
matching lines to categories, asking on Signal, remembering. This app keeps the parts that
need to be right — the schema, the validation, the history, and the commit. Building a CSV
parser and matcher in here would be a lot of effort to reproduce something Hermes already
does well, so it isn't in scope. What *is* in scope is making the data Hermes needs reachable
over a stable interface, so its matching is grounded in the committed history rather than in
its own recollection.

Hermes has no shell and no checkout; it talks HTTP. Writes are committed onward to the data
repo through the **GitHub Contents API** — the direction ARCHITECTURE.md §8 already
anticipated ("*POST /invoices/{n}/paid setting the paid date through the GitHub Contents
API*"), arriving for actuals first.

The app must **not** write its own `DATA_DIR`. That directory is a mounted checkout on an
ephemeral machine; a local write would be lost on restart and would diverge from git. Git
stays the single store, and the app is a validating gateway in front of it.

### Structure: one service, two adapters

`internal/api` holds the actual logic as plain Go methods over the existing
`tracker.Budget` / `tracker.Actuals` loaders plus a GitHub Contents client. Two thin
adapters sit on top:

- **REST** (`cmd/pocketcfo/api.go`) — plain JSON over `net/http`, for anything scripted.
- **MCP** (`internal/api/mcp.go`) — the same methods exposed as tools.

Neither adapter contains logic. Adding a read endpoint later means one service method and
two three-line registrations.

### Whole-month writes only

The data repo **deploys on commit**, so every accepted write restarts this app. A
per-transaction API would mean a redeploy per transaction. The unit of writing is therefore
one complete month document — Hermes assembles the full file and submits it once. This also
makes the endpoint idempotent and makes the anti-vanish diff in §8 trivial.

### Read endpoints

All under `/api/`, none of them touching the session cookie.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/budget/categories` | `[{id, group, category, kind}]` — the only legal `category` values. Hermes never parses `budget.json` itself. |
| `GET` | `/api/budget/{year}-{month}` | the plan for one month: every category with its `planned_cents` **for that month specifically**, overrides already applied. Uses `categoryCents` (`budget.go:329`), so the API can never drift from what the dashboard shows. |
| `GET` | `/api/budget/{year}` | the same for a year, via `Budget.ForYear`. |
| `GET` | `/api/actuals/{year}-{month}` | the committed month document plus its git blob `sha`. 404 when never imported. |
| `GET` | `/api/transactions?q=&from=&to=&category=&limit=` | **the one that replaces a rules file.** Full-text search over the description of every past transaction, returning each with the category it was assigned to. Hermes asks "have I seen `LIDL SOFIA 4412` before?" and gets the answer from committed history rather than from memory. |
| `GET` | `/api/reconciliation` | per month: coverage ranges, transaction count, planned vs actual. Tells Hermes where it left off. |
| `GET` | `/api/accounts` | account names from `accounts.json`, so a transaction's `account` field is spelled the way the rest of the system spells it. |

Read endpoints serve from the mounted checkout via the existing cached loaders — no GitHub
round-trip, no new failure mode.

### Write endpoint

`PUT /api/actuals/{year}-{month}` — body is the full month document plus
`{"base_sha": "...", "allow_removals": "..."}`. Pipeline, in order; every step rejects with a
specific error before anything reaches GitHub:

1. **Auth** — `Authorization: Bearer <token>`, compared with `subtle.ConstantTimeCompare`
   against `HERMES_API_TOKEN`. Not a session; `currentSession` is never consulted. Note the
   dev bypass in `cmd/pocketcfo/session.go` grants admin to every local request — the API
   must not inherit that, so its auth is written standalone rather than reusing
   `s.authorized`.
2. **Structural + business validation** — `ValidateActuals` against the current
   `budget.json`, exactly the same function the CLI and CI run.
3. **Anti-vanish diff** — §8, against the content currently in git.
4. **Optimistic concurrency** — `base_sha` must match the blob sha GitHub reports. Mismatch
   → `409`, and Hermes re-`GET`s and retries. Prevents two overlapping imports clobbering
   each other.
5. **Commit** — `PUT /repos/{owner}/{repo}/contents/actuals/{year}-{month}.json` with the
   app's own credential, a Conventional Commit message
   (`feat(actuals): reconcile August through 9 Aug`), and the override reason in a trailer
   when one was used.

Response: `200` with the new sha and a note that the deploy is pending — the app is about to
be restarted by its own commit, so its in-memory caches reset for free and the Reload link
isn't needed for actuals.

### MCP server — `/mcp`

The same service methods, exposed as MCP tools over streamable HTTP at `/mcp`. Hermes gets
typed tools with schemas instead of a URL convention it has to remember, which is the
difference between it reliably calling `search_transactions` and it improvising a query
string.

**Auth sits in front of the protocol, not inside it.** `/mcp` is wrapped in the same bearer
middleware as `/api/`, applied before a single byte of JSON-RPC is parsed — so `initialize`
and `tools/list` are refused too, and an unauthenticated caller learns nothing about the
tool surface, not even that it exists. No per-tool auth checks: one gate, at the mux. Like
`/api/`, the route is only registered when `HERMES_API_TOKEN` is set, and it never consults
the session cookie — which also means the local-dev admin bypass in
`cmd/pocketcfo/session.go:currentSession` cannot reach it. Explicit tests for both:
unauthenticated `tools/list` → 401, and local dev without a token → 404.

| Tool | Maps to |
|---|---|
| `list_budget_categories` | `GET /api/budget/categories` |
| `get_budget` (`period: "2026-08"` \| `"2026"`) | `GET /api/budget/{period}` |
| `get_actuals` (`month`) | `GET /api/actuals/{month}` |
| `search_transactions` (`query`, `from?`, `to?`, `limit?`) | `GET /api/transactions` |
| `get_reconciliation_status` | `GET /api/reconciliation` |
| `list_accounts` | `GET /api/accounts` |
| `put_actuals` (`month`, `document`, `base_sha`, `allow_removals?`) | `PUT /api/actuals/{month}` |

Tool descriptions carry the contract that would otherwise live in a doc: `put_actuals` says
in its own description that it is a **whole-month replacement**, that omitting a transaction
is a removal, and that a `409` means re-read and merge rather than retry.

**Dependency call, flagged.** This uses the official Go MCP SDK
(`github.com/modelcontextprotocol/go-sdk`) — a real departure from AGENTS.md's stdlib-first
rule, so it goes in that file's named-exception list beside `go-jsonschema`, with the reason
recorded: protocol-version negotiation, session handling and the streamable-HTTP framing are
fiddly and clients are strict. Hand-rolling JSON-RPC for a tools-only server is roughly 300
lines and zero dependencies if you'd rather keep the dependency list at three.

### Configuration

Two new env vars in `cmd/pocketcfo/config.go`, both **optional**: absent means the API
routes are simply never registered, the same nil-means-not-configured convention Toggl,
Budget and Accounts already use. Do *not* add them to `requireProdVars`.

| Var | Value |
|---|---|
| `HERMES_API_TOKEN` | shared bearer token for Hermes |
| `GITHUB_DATA_TOKEN` | fine-grained PAT (or GitHub App installation token) with **`contents: write` on the data repo only** — nothing else, no other repo |

The blast radius is deliberately one repo's contents, and every write it makes is a git
commit you can read and revert. That is the property that makes handing Hermes an API
tolerable: it isn't trusted, it's *audited*.

### `docs/HERMES.md`

Most of the contract lives in the MCP tool descriptions, where Hermes actually reads it.
This file is the workflow around them:

1. `list_budget_categories` — that list is the only legal set of `category` values.
2. `get_actuals` for the month — the committed file is the source of truth. Your own memory
   of recurring merchants is a starting point for *proposing*, never for overriding it.
3. For each statement line you can't place immediately, `search_transactions` on the
   description. A past match with an assigned category is a strong answer; no match means
   ask the user.
4. Every line gets exactly one of `category` or `ignored`. Never guess, never drop a line.
5. If the answer is "this needs a new budget category", ask the user to add it — Hermes does
   not write `budget.json`.
6. Derive `id` deterministically (short hash of account+date+amount+description) so a
   re-uploaded statement produces identical ids.
7. `put_actuals` submits the **whole month**, including every transaction already recorded.
   Anything you leave out is a removal and will be rejected.
8. `409` means the file changed underneath you: re-read, merge, resubmit. Never retry blind.

## 7a. The CLI stays

The same checks stay available offline, for hand-edits and for CI (§8). One
`ValidateActuals` implementation, three callers.

| Command | Purpose |
|---|---|
| `pocket-cfo-ctl actuals validate [dataDir]` | `ValidateActuals` over every `data/actuals/*.json` against `budget.json`; `--allow-removals <reason>` overrides the anti-vanish check. Non-zero exit on any breach. |
| `pocket-cfo-ctl actuals categories [dataDir]` | the category id table, for when you're editing by hand |
| `pocket-cfo-ctl actuals status [dataDir]` | per-month coverage and totals |

Also fold actuals into the existing top-level `pocket-cfo-ctl validate`
(`cmd/pocket-cfo-ctl/validate.go`).

## 8. Nothing vanishes silently

The real risk isn't a malformed file — validation catches that. It's a subtly *smaller*
file: Hermes rebuilds August from a statement that only covers the last two weeks, submits
it, and the first two weeks quietly cease to exist. Every figure still adds up. Nothing
looks wrong.

**The check.** Compare the incoming document against the one currently committed:

- a transaction `id` present before and absent now → **removal**
- an `id` present in both whose `date`, `amount`, `category` or `ignored` changed →
  **mutation**
- a `coverage` range that shrank → **coverage regression**

Any of the three rejects the write. Adding transactions, adding coverage, and adding a brand
new month are always fine — the common case never trips it.

**Implemented once**, in `internal/finance/actualsdiff`, as a pure function over two
documents:

```go
type Change struct { Kind string; ID string; Detail string } // "removed" | "mutated" | "coverage-shrank"
func Diff(before, after actualsdata.ActualsFile) []Change
```

**Enforced in three places**, all calling it:

1. **The API** (§7 step 3), diffing against what it fetched from GitHub. This is the one
   Hermes cannot route around.
2. **The CLI**, diffing the working tree against `git show HEAD:<path>`.
3. **CI on the data repo** — a workflow running `pocket-cfo-ctl actuals validate` on every
   push. The backstop for hand-edits and for anything that bypasses the API.

**The override**, because sometimes a removal is genuinely correct — a duplicated import, a
transaction that turned out to belong to a different month:

| Caller | How |
|---|---|
| API | `"allow_removals": "<reason>"` in the request body. Non-empty reason required. |
| CLI | `--allow-removals "<reason>"` |
| CI | an `Allow-Removals: <reason>` trailer on the head commit message |

The reason is never optional and never a bare boolean — it lands in the commit message
trailer, so `git log` records why anything ever disappeared. The API response and the CLI
both print exactly what was dropped, so an override is a decision you can see rather than a
flag that silences an error.

## 8. Session TTL

The Fly disconnects aren't Fly — sessions are stateless AES-GCM cookies, so a machine
suspend or restart cannot log you out. `auth.TTL` (`internal/auth/session.go:21`) caps a
GitHub session at **10 minutes**; the email magic-link tier gets 7 days. Raise `TTL` to
8 hours, update its doc comment and `ARCHITECTURE.md:586` ("10-minute TTL").

Separately, the "Authorize organisation" prompt is the OAuth app asking for org approval on
the `repo` scope — it appears once per org and goes away after you grant it, or entirely if
the data repo is personally owned rather than org-owned.

---

## Part A subtasks

Each is independently verifiable and gets one commit via the `ship-it` skill (AGENTS.md).

1. **Category ids** — schema, `ValidateBudget`, `budget add-ids`, migrate sample data, regenerate.
2. **Actuals schema + validator** — `actuals.schema.json`, `actualsdata` codegen,
   `ValidateActuals`, sample `data/actuals/2026-08.json`, table tests.
3. **Loader** — `tracker/actuals.go` + `actuals_test.go` (fstest.MapFS, mirroring
   `accounts_test.go`), wiring and eviction in `cmd/pocketcfo`.
4. **Dashboard display** — view fields, `buildBudgetView`, template, CSS, coverage note.
   Tests in `budget_test.go` / `view_test.go` must assert the existing totals are unchanged.
5. **Detail page** — route, admin gate, template, dashboard links.
6. **`actualsdiff` + CLI** — the diff function with table tests, `actuals` subcommands, the
   `--allow-removals` override, fold into top-level `validate`.
7. **Read API** — `internal/api` service methods over the existing loaders, bearer auth, the
   six read endpoints, config vars. Depends on 2–3. Independently useful: at this point you
   can already curl the budget and the history.
8. **Write API** — GitHub Contents client, `PUT /api/actuals/{month}`, optimistic
   concurrency, the anti-vanish gate. Depends on 6 and 7.
9. **MCP adapter** — `/mcp`, the seven tools over the same service, AGENTS.md exception
   note, `docs/HERMES.md`. Depends on 7 and 8.
10. **Data-repo CI** — a workflow in the *data* repo running `pocket-cfo-ctl actuals validate`
    with the commit-trailer override. Lands outside this repo; do it last, once the CLI's
    exit codes are settled.
11. **Session TTL** — one-line change plus docs. (Independent; can land any time.)

## Part A verification

```bash
make generate && go build ./... && go test ./... && go vet ./...
```

Then run the app against the sample data (`go run ./cmd/pocketcfo`, no env needed — local
dev auto-grants an admin session, `cmd/pocketcfo/session.go`):

- `/2026/8` with `data/actuals/` **removed** → page identical to today (diff the HTML
  against a capture taken before the change; this is the "nothing shown" requirement).
- `/2026/8` with the sample actuals present → actual column, coverage note, group actual
  totals. Confirm `Balance`, `Available to spend` and `Total private expenses` are byte-for-byte
  the same as without the file.
- Click a category's actual → lands on `/2026/8/spending#cat-…`, correct transactions,
  subtotals reconcile to the dashboard figure, ignored lines listed.
- `/2026/9` (no file) → no actuals UI at all.
- Break a category id in the sample actuals → dashboard shows `ActualsErr` inline without
  killing the page; `pocket-cfo-ctl actuals validate` exits non-zero naming the id.
- Admin gate: with `ENV=prod` and an email-OTP session, `/2026/8/spending` returns 404 and
  the dashboard shows actual amounts as plain text with no link.
- `pocket-cfo-ctl actuals categories` / `status` / `validate` against `data/`.
- `docker build -t pocket-cfo .` to confirm the sample data still ships clean.

API, against a scratch data repo — never the real one until every case below passes:

- No `HERMES_API_TOKEN` set → `/api/...` and `/mcp` are 404. The routes don't exist unconfigured.
- Wrong/absent bearer token → 401. Correct token → 200.
- `GET /api/budget/2026-08` returns the same per-category figures the dashboard renders for
  August, overrides included. Assert this against `Budget.ForMonth` in a test — the API
  drifting from the UI is the failure mode that would quietly poison Hermes' matching.
- `GET /api/transactions?q=LIDL` finds a committed transaction from a previous month and
  reports the category it was assigned to. Empty query, no match, and a `limit` cap all
  behave.
- `/mcp`: connect a real MCP client, confirm `tools/list` returns all seven with schemas,
  and drive one full reconciliation through the tools end to end — `list_budget_categories`
  → `search_transactions` → `put_actuals`.
- Local dev (`ENV` unset) must **not** grant API access via the session bypass in
  `cmd/pocketcfo/session.go:currentSession`. Explicit test for this.
- `PUT` a valid month → commit lands in the scratch repo, message is conventional, response
  carries the new sha.
- `PUT` the same body twice with a stale `base_sha` → 409, nothing committed.
- `PUT` a body missing one transaction that's already committed → 400 naming the dropped id,
  nothing committed. Resubmit with `"allow_removals": "duplicate import"` → accepted, and
  the reason appears in the commit trailer.
- `PUT` a body with a shrunk `coverage` range → same rejection path.
- `PUT` referencing an unknown category id → 400 before any GitHub call.
- `GET /api/budget/categories` returns every id in `budget.json` and nothing else.
- Unit-test `actualsdiff.Diff` directly: additions clean, removal caught, each mutated field
  caught, new month clean.

---

# Part B — payment leaves the invoice, annulment leaves the codebase

## Context

On the invoicing dashboard, INV-0000000003 shows **STALE** despite nothing being wrong with
it, and the draft shows **STALE** too. Two different causes.

`IsCurrent` (`internal/render/staleness.go:22`) already hashes *rendered HTML* rather than
raw JSON, precisely so that setting `paid` wouldn't stale the archived original — the
original is always rendered as-if-unpaid, so the field never touches its content. But it
then adds a second condition: once `inv.Paid != nil`, `INV-…-paid.pdf` must also exist in
the manifest **and** match. Marking an invoice paid therefore reddens the row until you
re-run `pocket-cfo-ctl render`. That's a "PDF not built yet" fact wearing the costume of a
"your JSON drifted" warning.

The root cause is structural: `paid` is mutable state living inside a document that is
supposed to be immutable once issued. Every write to it churns a file whose whole value is
that it doesn't change. Moving payment into its own file makes the invoice genuinely
write-once.

Annulment goes at the same time — it's schema, docs and one filter in `stats`, with no
renderer, no route and no real instance yet. Carrying an unexercised design through this
refactor costs more than re-adding it when the first annulment actually happens.

Decisions taken:

- **The badge judges the original only.** The `-paid.pdf` check comes out of `IsCurrent`.
  The badge answers exactly one question: does the archived original still match its JSON.
- **Drafts get no badge at all.** A draft is work in progress; "stale" is meaningless.
- **`data/paid-invoices.json` is an array of objects**, leaving room to attach a bank
  reference later — which is how a Part A statement credit could eventually be linked to
  the invoice it settled.

## 1. The new file

**`data/paid-invoices.json`**

```json
{
  "$schema": "../schemas/paid-invoices.json",
  "paid": [
    { "invoice": "INV-0000000001", "date": "2026-04-20" },
    { "invoice": "INV-0000000002", "date": "2026-07-14" },
    { "invoice": "INV-0000000003", "date": "2026-08-06" }
  ]
}
```

**`schemas/paid-invoices.json`** — `invoice` matches `^INV-\d{10}$`, `date` is a date,
`additionalProperties: false`, `note` optional. Generated into
`internal/schema/paidinvoices/` by the existing `internal/schema/generate.go` (add it to
that file's `//go:generate` list). Embedded like every other schema (AGENTS.md).

Hand-validated in `cmd/pocket-cfo-ctl/validate.go`: no duplicate invoice numbers, every
number resolves to an existing invoice, and no draft is marked paid.

## 2. Invoice schema shrinks

**`schemas/invoice.json`** — remove `paid` and `annulment` from `properties` and from
`required`; delete `$defs/annulment`. Regenerate `internal/schema/invoice`
(`make generate`) — this drops the `Paid`/`Annulment` fields and the two required-field
checks in the generated `UnmarshalJSON` (`invoice.go:316`, `:340`).

Unknown JSON keys are ignored by `encoding/json`, so un-migrated invoice files keep parsing
throughout — the migration below is about not losing the dates, not about unblocking reads.

## 3. Threading the payment date through

`paid` currently flows from `inv.Paid` into four places. Each takes the date as a parameter
instead. New loader in `internal/stats`, alongside the existing `LoadInvoices`/`LoadRecipients`:

```go
// LoadPaid reads paid-invoices.json into invoice number -> payment date.
// A missing file means nothing has been paid yet, not an error — same
// optional-layer convention as accounts.json.
func LoadPaid(path string) (map[string]types.SerializableDate, error)
```

| Call site | Change |
|---|---|
| `internal/render/render.go:52` | `HTML(inv, totals, showPaid bool)` → `HTML(inv, totals, paidOn *types.SerializableDate)`; nil means render the original. Collapses the flag and the date into one argument, which removes the current possibility of `showPaid=true` with no date. `render.Data` gains `PaidOn`. |
| `templates/invoice.html.tmpl:136` | `{{dateptr .Invoice.Paid}}` → `{{dateptr .PaidOn}}` |
| `internal/render/staleness.go` | `IsCurrent(inv, totals, manifest)` keeps its signature and **loses lines 30-34 entirely** — no `-paid.pdf` check |
| `cmd/pocket-cfo-ctl/render.go:315` | `targetsFor(inv)` → `targetsFor(inv, paidOn)`; the `-paid.pdf` target is added when `paidOn != nil` |
| `internal/stats/stats.go:111,222,249` | `deriveState` and `Aggregate` take the paid map |
| `cmd/pocketcfo/main.go:230-278` | `loadInvoicingView` loads the paid map and passes it to `stats.Aggregate` |
| `cmd/pocketcfo/client.go:112,120` | portal reads the map |

**The rendered HTML must stay byte-identical.** Same `dateptr` function, same date value, so
the stamped copy's hash is unchanged and `build/render-manifest.json` stays valid. No PDF
needs re-rendering because of this change. Assert it with a test that renders a fixture
before and after and compares hashes.

## 4. Annulment removal

- `internal/stats/stats.go:71` — drop the `if inv.Annulment != nil { continue }` skip;
  `LoadInvoices` no longer filters anything.
- `internal/stats/stats_test.go:39,48,57,163` — update fixtures, drop the annulled case.
- `cmd/pocket-cfo-ctl/delete.go` — check the "issued invoices are annulled, not deleted"
  wording still makes sense; drafts-only deletion is unchanged either way.
- **ARCHITECTURE.md** — §3.7 (the annulment spec), the state table at §302, the
  `-ANUL.pdf` rows at §24/§64/§415/§499/§508, the `/invoices/{number}-ANUL.pdf` route at
  §617, the test note at §679, roadmap item 9 at §702. Replace §3.7 with a short
  "deliberately deferred" note recording *why* (no instance yet, no renderer written) and
  what comes back when the first one appears, so the design isn't lost — just unbuilt.

## 5. Draft rows show no badge

`templates/index.html:83-88` renders the `pdf-status` span unconditionally. A draft's
`-DRAFT.pdf` is re-rendered on every `render` run (`targetsFor`, `overwrite: true`), so any
edit since the last run correctly reports "not current" — but for a document you're still
writing, that's noise, not a warning. Wrap both branches in `{{if ne .State "draft"}}`.

## 6. Migration

`pocket-cfo-ctl invoices extract-paid [dataDir]` (new, `cmd/pocket-cfo-ctl/invoices.go`),
run once against the real data checkout:

1. Read every `data/invoices/*.json` as raw JSON (not the generated struct — the fields are
   gone from it by then).
2. Collect every non-null `paid` into `data/paid-invoices.json`, sorted by invoice number.
3. Strip `paid` and `annulment` from each invoice file, rewriting with
   `json.MarshalIndent` + `os.WriteFile` — the same in-place pattern as
   `cmd/pocket-cfo-ctl/translate.go:97-103`.
4. Refuse to run if `paid-invoices.json` already exists, so it can't clobber.

Stripping those two keys does **not** change any rendered HTML, so no manifest entry is
invalidated and nothing needs re-rendering.

Update the sample `data/invoices/INV-000000000{1,2}.json` and add the sample
`data/paid-invoices.json` in the same commit.

## Part B subtasks

1. **`paid-invoices.json`** — schema, codegen, `stats.LoadPaid`, validator rules, sample
   data. Nothing reads it yet.
2. **Thread the date through** — `render.HTML`, the PDF template, `targetsFor`, `stats`,
   `cmd/pocketcfo`; remove `paid` from `schemas/invoice.json`; regenerate. Includes the
   byte-identical-HTML test.
3. **Badge semantics** — drop the `-paid.pdf` check from `IsCurrent`, hide the badge on
   drafts, update `staleness_test.go` (`:128` sets `inv.Paid`).
4. **Annulment removal** — schema, `stats`, tests, ARCHITECTURE.md.
5. **`invoices extract-paid`** — migration command plus a test over a fixture data dir.

## Part B verification

```bash
make generate && go build ./... && go test ./... && go vet ./...
```

- `pocket-cfo-ctl render --dry-run` against the sample data: **renders nothing**. This is the
  central assertion — it proves the paid split changed no HTML and invalidated no manifest
  entry.
- `/invoicing` in the app: INV-0000000003 shows `paid` with its date and **no stale badge**;
  the draft row shows **no badge at all**; INV-0000000001/2 still show "up to date".
- Add a new entry to `data/paid-invoices.json` by hand → the row flips to paid, the badge
  does not change. Run `pocket-cfo-ctl render` → the `-paid.pdf` appears and the `(paid)`
  link lights up.
- Edit an issued invoice's `title` → that row correctly goes stale. The badge still detects
  real drift.
- `pocket-cfo-ctl validate` rejects: a duplicate invoice number in `paid-invoices.json`, a
  number with no matching invoice, and a draft marked paid.
- `extract-paid` over a copy of the current `data/`: dates all present, invoice files no
  longer contain `paid`/`annulment`, second run refuses.
- Client portal (`/invoicing/client/{token}`) still shows the paid badge for a paid invoice.

---

# Part C — Toggl timeouts block the dashboard

## Context

```
Post "https://api.track.toggl.com/reports/api/v3/workspace/21443791/search/time_entries":
context deadline exceeded (Client.Timeout exceeded while awaiting headers)
```

The timeout is the symptom. Four things combine to make it as bad as it is:

1. **One HTTP client for everything.** `cmd/pocketcfo/main.go:94` builds a single
   `&http.Client{Timeout: 15 * time.Second}` and hands it to SES, GitHub OAuth, the holiday
   API *and* Toggl. 15s is right for an OAuth callback and wrong for a year-wide Toggl
   report, which is the slowest thing this app does by a wide margin.
2. **The report is paginated, the budget is not.** `eachDetailedRow`
   (`internal/finance/tracker/toggl.go:159`) loops pages, each a fresh POST with its own 15s
   ceiling, while the whole request shares one `requestTimeout = 20 * time.Second`
   (`cmd/pocketcfo/finance.go:146`). A multi-page year blows the request budget even when no
   single page times out.
3. **Only successes are cached.** `getCached` (`toggl.go:46`) stores nothing on error, so
   every reload retries the whole slow fetch from cold — and each retry adds load to an API
   that's already slow or rate-limiting. There's no retry, no backoff, and no 429 handling
   (`apiError` at `toggl.go:456` just formats the status).
4. **The fetch is inline in the request.** So the page holds for 20s and then renders with
   the Income panel blanked. As you say: it's a blocking call that has no business blocking.

## The fix, in order of value

**1. Keep the last good data.** Today one transient timeout empties the Income panel. Change
`getCached` to retain the previous value and return it on a failed refetch, flagged stale —
`Figures.LastUpdated` already exists to tell you how old it is. This alone converts almost
every occurrence of this bug from an outage into a slightly out-of-date number.

**2. Give Toggl its own client.** A second `*http.Client` with a 60s timeout, used only by
`Toggl`. Leave the shared 15s one for SES/OAuth/holidays, where fast failure is correct.
`buildTracker` (`main.go:62`) already takes the client as a parameter, so this is a
signature change, not a refactor.

**3. Retry with backoff.** Wrap `do` (`toggl.go:437`): retry on timeout, 5xx and 429, three
attempts, exponential backoff, honouring `Retry-After` when present. Toggl rate-limits
reporting endpoints and currently the app just surfaces the 429 as an error.

**4. Single-flight the fetch.** Two concurrent loads of the same year currently fire two
identical year-wide reports. One in-flight fetch per cache key, other callers wait on it.

**5. Take it out of the request path.** Warm the current year on startup and refresh it on a
ticker (default 15 min, `TOGGL_REFRESH_INTERVAL`). Requests then serve from cache, and
combined with (1) the page stops depending on Toggl being up at the moment you load it.

Cold start is the remaining case — Fly scales to zero, so the first request after an idle
period has an empty cache. Rather than block for 20s, give a *request* a short patience
budget (5s); if the fetch hasn't landed, render the page with the Income section marked
pending and a `<meta http-equiv="refresh" content="5">` so it fills in on its own. No JS —
the app has eight lines total and this doesn't need a ninth.

**6. Circuit-break.** After three consecutive failures for a key, stop attempting for a
minute and serve stale immediately. Stops a Toggl outage turning into a slow-request pile-up.

## Part C subtasks

1. **Stale-on-error cache** — `getCached` retains the last good value; `Figures` distinguishes
   "no data" from "stale data"; the dashboard says which. Tests with a fake that fails after
   one success.
2. **Dedicated client + retry/backoff/429** — separate Toggl client, `do` wrapper, table tests
   over a `httptest` server returning 429/500/timeout.
3. **Single-flight + circuit breaker** — concurrency test asserting one upstream call for N
   simultaneous readers.
4. **Background warm + pending render** — startup prefetch, ticker, per-request patience
   budget, the meta-refresh pending state.

## Part C verification

- Point `TOGGL_API_TOKEN` at the real workspace and load `/` repeatedly: no 20s hangs, and
  `LastUpdated` advances on the ticker without a Reload click.
- `httptest` server that sleeps past the timeout: first load renders pending and self-refreshes;
  once one fetch succeeds, later failures show the stale figure plus its timestamp, never a
  blank panel.
- `httptest` server returning 429 with `Retry-After: 2`: the client waits and succeeds rather
  than surfacing an error.
- Ten concurrent requests for the same year → exactly one upstream call.
- Kill the upstream entirely → page still renders in well under a second, with stale data and
  an honest note.
- Confirm SES and the OAuth callback still fail fast — they must not inherit the 60s timeout.
