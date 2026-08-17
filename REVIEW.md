# PocketCFO — Full Code Review

**Date:** 2026-08-17
**Scope:** Entire Go codebase — five domain reviews: invoicing core (`money`/`tax`/`stats`/`schema`), render pipeline + `pocket-cfo-ctl`, finance tracker (`internal/finance`), web app + auth (`cmd/pocketcfo`, `auth`/`mail`/`users`/`webui`), agent API (`internal/api`).
**Method:** Each domain reviewed against its spec (ARCHITECTURE.md §1–13, docs/HERMES.md); every finding verified by reading the actual code path. Generated code (go-jsonschema output) excluded.

**Totals:** 1 critical · 7 high · 17 medium · 41 low.

> **Status: acted on.** Every finding was re-verified against the code before being fixed;
> the corrections are recorded in "Verification notes" at the end of this file. Two findings
> were wrong, four materially overstated, and one prescribed a fix that would have broken
> what it repaired. Everything that survived verification has been fixed.

**Executive summary:** The computation cores (money arithmetic, salary cascade, roll-forward) are correct, internally consistent, and pinned by tests that assert documented invariants. The recurring failure pattern is at the boundaries: JSON-schema keywords the generator silently ignores, validators that fail open, and docs (§4.3, §5.1, §6, HERMES.md) promising enforcement the code doesn't deliver. Nothing requires redesign — the fixes are enforcement, fail-closed error handling, and schema constraints.

---

## Critical

### C-1 — `internal/tax/` contains no code at all; the §4 tax-resolution safety property is absent
**Location:** `internal/tax/.gitkeep` (only file); spec at ARCHITECTURE.md:351-361, test mandate at ARCHITECTURE.md:670

`tax.Resolve(issuer, recipient) → Regime` does not exist, so the "EU consumer must fail loudly, never silently zero-rate" branch cannot be verified — there is no branch. Nothing cross-checks a hand-edited invoice's stored `tax.regime` against issuer/recipient countries: an invoice with `outside_eu_place_of_supply` snapshotted onto a BG domestic recipient (silently zero-rating what should be a 20% invoice) passes `pocket-cfo-ctl validate` and renders. The mandated table tests (CH, AT, DE, BG, GB, US, EU-consumer) are likewise missing. Git history shows the package never contained code.

---

## High

### H-1 — `money.Compute` panics with "decimal division by 0" on schema-valid hand-edited JSON
**Location:** `internal/money/money.go:137` (guard that fails to cover it at `:81`)

`allocateDiscount` divides by `subtotal` in the multi-group branch (`len(groups) > 1 && totalDiscount != 0`). Reachable with subtotal == 0: two zero-priced lines in different VAT groups (`{"unit_price": 0, "vat_rate": 0}` and `{"unit_price": 0, "vat_rate": 20}` — `unit_price` has no `minimum` in the schema) plus a negative discount `amount: -100` (also schema-valid, see M-1). Then `net = +100` so the `net < 0` guard passes, `totalDiscount = -100 ≠ 0`, and `Div(decimal.NewFromInt(0))` panics (shopspring/decimal `QuoRem` panics on a zero divisor). Because `stats.Aggregate → money.Compute` runs at web request time (`cmd/pocketcfo/main.go:191`) and in CI render, one hand-edited file kills the request/build. Needs a `subtotal == 0` guard in `allocateDiscount` and/or schema minimums. *(Found independently by two reviewers.)*

### H-2 — `validate` enforces almost none of ARCHITECTURE.md §4.3 — the "entire safety net" is mostly absent
**Location:** `cmd/pocket-cfo-ctl/validate.go:22-64`

The command only does `json.Unmarshal` into generated structs plus `ValidateBudget`/`ValidateAccounts`/`ValidatePaid`/`ValidateActuals`. The generated unmarshalers check required fields and JSON types but **not** `pattern`, `enum`, `minLength`, `additionalProperties`, `oneOf`, or `minimum`/`maximum` — so `"number": "GARBAGE"` (breaks `^INV-\d{10}$`), `"status": "issuedd"`, `"currency": "eur"`, `"vat_rate": 250`, a typo'd extra key, or a discount with both `percent` and `amount` all pass. Of the §4.3 business rules, only the paid-invoices checks exist: there is no gapless-sequence/duplicate-number check, no filename↔`number` check, no `due_date >= issue_date` check, no discount running-total check, no translation-completeness check, and `mandatory_wording` appears in **no Go file at all** even though §4.3 calls "обратно начисляване" a hard legal content requirement (чл. 114). `catalog/notes.json` is also never validated despite §4.3 covering `catalog/`. This runs as CI step 1 (`build.yml`), so all of the above reaches the render pipeline unchecked.

### H-3 — Auth fails open when `ENV` is anything other than exactly `"prod"` — full unauthenticated admin, including client-portal bearer secrets
**Location:** `cmd/pocketcfo/session.go:19-22` + `cmd/pocketcfo/config.go:91-95, 98-108`

`currentSession` returns a synthetic `admin` session for *every* request whenever `cfg.env != "prod"`, and `requireProdVars` (the only thing that would catch missing secrets/base URL) is gated on that same check. If the deployed container is started with `ENV` unset, empty, or a typo (`production`, `Prod`), the entire app — dashboard, all invoice PDFs, `/info` (masked credentials + config), and crucially `portalLinks()` on `/invoicing` (main.go:203-206), which are the bearer-secret URLs for the `/client/{token}` tier — is served to the whole internet with no login and no log warning. README.md:155 documents this deliberately, but a single env-var typo silently converting the app to world-readable admin is a fail-open design; the safe default would be to require an explicit `ENV=development` to disable auth and refuse to boot otherwise.

### H-4 — `gitShow` fails open: any git error silently disables the committed-data-destruction check
**Location:** `cmd/pocket-cfo-ctl/actuals.go:142-152`

`git show ref:path` exits 128 identically for "path not in tree at ref" (the intended `ok=false` case), "`dir` is not a git repository", and "invalid object name" (typo'd/missing `--base-ref`, or a shallow clone lacking the ref). The code maps *every* `*exec.ExitError` to `(nil, false, nil)`, so in all three failure cases the per-month diff is silently skipped and `actuals validate --base-ref HEAD` prints `OK` and exits 0 — the exact outcome the flag exists to prevent. The tests (`actuals_test.go:107-157`) only cover the happy paths inside a real repo. Stderr must be inspected to distinguish "path does not exist in ref" from other 128s.

### H-5 — Pre-anchor months are cascaded with a company balance that is not yet in force, and the loan accrues the error
**Location:** `internal/finance/tracker/accounts.go:247-265`

When the director's loan is anchored *earlier* than the bank readings (the ordinary shape: loan restated at year-end for December, bank read monthly), `walkStart` returns the loan's anchor, so the loop walks months before `snap.OpensMonth`. For those months `monthClose` is called with `open.Company` initialized from the snapshot (line 249) — a reading that only opens later — because the reassignment at line 262 is gated but the *input* at line 253 is not. The cascade's `spendableEUR()` therefore includes the future balance in every pre-anchor month, and `closed.NetIncomeCents` — computed from that phantom balance — is accrued into the loan at line 265. Concrete scenario: company reading of 6,800 dated 2026-07-31, loan reading of 12,400 dated 2025-12-31, viewing September 2026 — January through July each size their full salary with an extra 6,800 of spendable, overstating each month's net income (and hence the loan, and `company worth = cash − loan`) by the after-tax value of the balance, contradicting the documented "a month before every reading still has no balance." No test exercises `rollForward` with the two anchors in this order (every `loanTracker` fixture anchors both at 2026-07-31), so nothing pins it.

### H-6 — `movement` values are never validated against the enum, so a typo'd marker is committed and then silently moves no figure
**Location:** `internal/finance/actualsdata/validate.go:145-166` (and the write paths at `internal/api/add.go:263-273`, `internal/api/edit.go:203-230`)

`movementProblems` checks the ignored-beside-it rule, the splits conflict, and the sign via `directionProblem`, but nothing checks that the value is one of the six schema enum values (`actuals.schema.json:132-143`); `directionProblem` treats any unknown value as "money out", so `"movement": "owner_dra"` with a positive amount and an ignored reason passes `ValidateActuals` and is committed. The tracker then computes nothing for it: `Crossed()`/`MovedCompanyCash()`/`CrossedOutsidePayroll()` all return false and `movementTotals` (`internal/finance/tracker/actuals.go:291-309`) only emits the six known values, so the director's loan is never settled by the line — while `search_transactions only_movements` (`p.Movement != ""`) still shows it as marked, confirming the agent's false belief. The committed file also violates the schema its `$schema` key points at, and nothing at runtime ever says so.

### H-7 — The explicitly-pinned "two stacked 10% discounts take 19%" case has no test
**Location:** `internal/money/money_test.go:82-111`

ARCHITECTURE.md §3.5 (line 252) says the rule is "genuinely ambiguous, so it's stated here and pinned by a test", and §9 (line 672) lists it first. `TestCompute_StackedDiscounts` uses 2% + a *fixed amount*; the fixed amount is identical under either interpretation, so if someone refactored percent discounts to apply against the original subtotal instead of the running total, every existing test still passes. The canonical 10%+10%→19% case is simply absent.

---

## Medium

### M-1 — `discount.amount` has no `minimum`, so a negative "discount" silently inflates net, VAT bases, and grand total
**Location:** `schemas/invoice.json:136`; applied at `internal/money/money.go:72-73`

A hand-edited `"amount": -10000` passes validation (`percent` has `minimum: 0`; `amount` has nothing), and `running -= deduct` then *adds* 100,00 € to the invoice. The only runtime guard (`net < 0`, money.go:81) fires in the wrong direction, and §4.3 makes these validators the entire safety net for hand-editing. This is also the enabler of H-1. Fix is one line: `"minimum": 0` (or 1), mirroring `percent`.

### M-2 — The discount `oneOf`, the `const` fields, `propertyNames`, and `uniqueItems` are not enforced by the generated validator
**Location:** `schemas/invoice.json:138-141` (oneOf), `:23` and `:26` (const); `schemas/notes.json:13-15` (propertyNames); `schemas/users.json:14,28` (uniqueItems); silent preference at `internal/money/money.go:67-76`

go-jsonschema v0.24.1's validation matrix marks `oneOf`, `not`, `const`, `propertyNames`, and `uniqueItems` as unimplemented, so the generated `UnmarshalJSON` ignores them. Consequences: a discount with *both* `percent` and `amount` validates and money.go's `switch` silently prefers percent, discarding the amount; a discount with *neither* validates and only errors later inside `money.Compute` (at render/request time), not in `pocket-cfo-ctl validate` as §4.3 requires; `"schema_version": {"const": 1}` and `"type": {"const": "invoice"}` validate nothing — a `schema_version: 2` file sails through the version gate; a misspelled regime key in the accountant-owned catalog (e.g. `eu_b2b_reverse_charg`) validates silently — the correct entry is then simply missing at lookup time; duplicate users/parts arrays pass. The regime-key enum *is* coherent with `invoice.json`'s `tax.regime` enum (line 150); it just isn't machine-enforced.

### M-3 — No test distinguishes per-group from per-line VAT rounding
**Location:** `internal/money/money_test.go:116-158`

§3.4/§9 mandate "VAT rounds once per group, not per line", but every VAT test uses figures where both approaches agree (72000 × 20% is exact). A per-line-rounding regression — e.g. two 3-minor-unit lines @20%: per-line `round(0.6)+round(0.6) = 2` vs correct per-group `round(1.2) = 1` — would pass the whole suite undetected.

### M-4 — One computationally invalid invoice, including a draft, takes down the entire invoicing dashboard
**Location:** `internal/stats/stats.go:129` (via `computeAll`), surfaced at `cmd/pocketcfo/main.go:191-200`

`Aggregate` runs `money.Compute` over *every* invoice including drafts, and any error aborts the whole aggregation; `loadInvoicingView` propagates it, so the recipients/invoices view 500s (same for `finance.go:44`, `client.go:94`). A schema-valid draft with an oversized discount (`amount: 999999999` — exactly what `TestCompute_DiscountBelowZero` covers) is enough. Failing loud is right for issued invoices, but a half-edited draft — work-in-progress by definition — should not poison all derived state.

### M-5 — api2pdf request omits `outputBinary: true`, contradicting §6 and routing every invoice through the 24-hour file store
**Location:** `internal/render/api2pdf.go:42-45, 179-210`

`api2pdfRequest` has only `html` and `options.delay`; the code parses `FileUrl` from the response and GETs it **without** the Authorization header (i.e., the URL is a bearer token) — exactly the flow §6 says to avoid ("`outputBinary: true` to get bytes back inline and skip their 24-hour file store"). Invoice PDFs (names, addresses, VAT IDs, IBANs) therefore sit on third-party storage for 24h, and the client will follow redirects from an API-supplied arbitrary URL (a minor SSRF surface from CI runners; only the `%PDF-` prefix check at line 207 keeps fetched bytes out of the repo).

### M-6 — A corrected payment date never reaches the paid PDF — the §5.1 "JSON is newer" rule is unimplemented
**Location:** `cmd/pocket-cfo-ctl/render.go:171-183, 264-279`

The `-paid.pdf` target has `overwrite: false` and the skip decision is purely `os.Stat` existence, so once rendered it is only ever re-rendered by `--force` or manual deletion. There is no git-commit-timestamp comparison anywhere in the render pipeline (`git log -1 --format=%ct` appears in no Go file), so the "or if the JSON is newer" half of §5.1's third rule does not exist: edit the date in `paid-invoices.json`, push, and CI keeps stamping the old date. The manifest can't catch it either — `IsCurrent` deliberately never examines the paid artifact (`internal/render/staleness.go:8-23`, test comment at `staleness_test.go:126-128`).

### M-7 — Non-atomic PDF write + existence-based skip makes an interrupted render permanently "current"
**Location:** `cmd/pocket-cfo-ctl/render.go:209-212, 171-183`

`os.WriteFile(t.path, pdf, 0o644)` writes in place; if the process is killed (Ctrl-C, OOM, CI timeout) after partial bytes land, the next run sees the file exist, prints "skip … already rendered", and — worse — `backfillManifestEntry` (lines 218-237) then records the hash of the *current* HTML against a truncated PDF that does not represent it, so even the web staleness badge goes green. Write to a temp file in `build/` + `os.Rename`, and the whole class disappears.

### M-8 — Logo path from invoice JSON is read from an arbitrary filesystem location and inlined unescaped into HTML sent to api2pdf
**Location:** `internal/render/render.go:130-142, 61-68`

`inv.Issuer.Logo` is passed straight to `os.ReadFile` (no confinement to the repo, no `.svg`/content check beyond stripping an `<?xml` prolog) and returned as `template.HTML`, bypassing `html/template` escaping. A typo'd or malicious path (`/etc/passwd`, `~/.ssh/...`) has its contents embedded in the rendered HTML and POSTed to `v2.api2pdf.com`; a non-SVG file with markup is injected raw into the Chrome render context. Nothing in `validate` constrains the field.

### M-9 — `actuals validate` silently drops flags given after the positional arg
**Location:** `cmd/pocket-cfo-ctl/actuals.go:42-48`

Unlike `render`/`delete`/`budget ids` (which use `splitFlags` precisely because `flag.Parse` stops at the first non-flag arg — see the regression test at `render_test.go:14-18`), `runActualsValidate` calls `fs.Parse(args)` directly. `pocket-cfo-ctl actuals validate data --base-ref HEAD` parses `--base-ref` as a positional, leaves `baseRef == ""`, skips the destruction check, and exits 0 — the user believes the guard ran. Extra positionals are likewise ignored without error.

### M-10 — Email-login allowlist is enumerable via response timing, contradicting the documented anti-enumeration design
**Location:** `cmd/pocketcfo/email_login.go:26-34` (call site), `internal/mail/ses.go:26-45` (synchronous SES `SendEmail`)

ARCHITECTURE.md §8 states the generic "check your email" response prevents enumeration, but `sendLoginLink` is invoked synchronously in the request path only when the address is allowlisted, and it performs `config.LoadDefaultConfig` plus a network round trip to SES (typically 100–500 ms). A non-allowlisted address returns in ~1 ms. An attacker measuring response times can enumerate every address in `users.json` despite the identical response body. The send should be dispatched in a goroutine with a detached context (or an equal-cost dummy delay added on the reject path).

### M-11 — The HTTP server has no timeouts — Slowloris connection exhaustion
**Location:** `cmd/pocketcfo/main.go:99`

`http.ListenAndServe(addr, mux)` uses a zero-value `http.Server`: no `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, or `IdleTimeout`. Every outbound client in the app has a timeout, but the inbound server has none, so a trickle of slow-header/slow-body connections from a single host can exhaust file descriptors and take the app down. This is only mitigated if the hosting platform's load balancer happens to enforce its own — nothing in the repo guarantees that.

### M-12 — An open-ended `fixed` salary is validated against only the first month's minimum wage
**Location:** `internal/finance/tracker/salary.go:221-231` (`eachMonthOf`)

For a period with no `to`, `eachMonthOf` checks only `p.From`, so `requireFixedClearsTheMinimumWage` never sees later months. Minimum wages rise via new dated legislation entries; once the wage rises above an open-ended fixed amount, every subsequent month pays an illegal salary with no refusal anywhere — `grossSalaryFor` (personal.go:392) applies no floor to `fixed` at compute time either. This silently breaks the documented rule "fixed … may not be below the minimum wage in force" under completely ordinary config evolution. The same root cause weakens `requireMinimumWageThroughout` for open-ended `minimum` periods and `RequireMinimumWageForTargets` (target.go:166-178), though those only bite if a wage is explicitly zeroed later.

### M-13 — The agent-facing dividend report charges 0% in a month with no rate in force, bypassing the refusal the page enforces
**Location:** `internal/finance/tracker/dividends.go:129-153` (`DividendsIn`)

`DividendsIn` calls `r.CompanyProfitTax.on(gross)` / `r.DividendTax.on(gross)` directly; with nil bands these return 0, so the report presents a dividend with computed zero taxes. The design explicitly refuses this on the page (`unrated`, dividends.go:63; enforced in personal.go:435 and view.go:495) because "0% presented as a computed figure is not [a right figure]" — and `DividendClearing` in the same file (line 164) does guard nil rates, so the file is internally inconsistent. An agent reading `BudgetForMonth`/`BudgetForYear` (service.go:114) for an un-legislated month gets plausible-looking zeros for both taxes, `NetToOwnerCents`, and `CashNeededCents`.

### M-14 — The REST search adapter silently drops `only_untracked` and `only_movements`, so REST and MCP disagree on a filter HERMES.md relies on to prevent double-marking
**Location:** `cmd/pocketcfo/api.go:225-234`

The `api.SearchQuery` literal maps every field except `OnlyUntracked` and `OnlyMovements`; the query params are never read, and no test in `cmd/pocketcfo` references them. HERMES.md:58-59 tells the agent `only_movements` "is how you avoid marking the other side by mistake" and HERMES.md:68-69 points at `only_untracked` for outstanding cash — an agent on the REST surface gets unfiltered results with no error, can conclude a transfer is unmarked, and mark the mirror line, double-counting the loan settlement. This directly contradicts HERMES.md:6 ("Both surfaces expose the same service, so they never disagree").

### M-15 — The actuals write paths' "check your own output" guard is struct-blind: `marshalMonth` silently strips any field the generated structs don't know, and `refuseDestruction` cannot see the loss
**Location:** `internal/api/write.go:33-84` (marshalMonth), `internal/api/write.go:133-149` (refuseDestruction), `internal/api/write.go:27` (loadMonth's plain `json.Unmarshal`)

The document is rebuilt field by field, so any unknown root- or line-level field — a future schema addition, or one a human added by hand — is dropped from every line on the next add/edit, and `actualsdiff.Diff` compares parsed `ActualsFile` structs, which never contained the unknown fields in either version, so the self-check passes and the stripped file is committed. The sibling write paths do not have this blind spot: `verifyOnlyTheReadingWasAdded` (`balance.go:366-391`) and `verifyOnlyDateChanged` (`moveexpense.go:278-304`) compare full-JSON `any` trees, which do see unknown fields. `movement_test.go:26-30` shows the team knows the mechanism ("a field it does not know about is not preserved — it is dropped from every line in the month") — but the guard only covers the fields that exist today, so the next `movement` repeats the bug through the path explicitly advertised as self-checking (ARCHITECTURE.md:1018-1019).

### M-16 — Coverage ranges may claim days that haven't happened yet, flipping a month to "complete" while part of it is still unspent
**Location:** `internal/api/add.go:199-216` (groupCoverageByMonth), `internal/finance/actualsdata/validate.go:33-56` (coverageProblems)

Coverage is checked for real dates, `to >= from`, and containment in the month — but never against `now`: on 2026-08-17 an agent can send `to: 2026-08-31`, and `coverageComplete` (`internal/finance/tracker/actuals.go:311-353`) then reports the month complete, so the dashboard stops withholding under-budget judgement with two weeks of spending unaccounted — the exact failure coverage exists to prevent (HERMES.md:241-243). The balance path refuses the same class of input explicitly (`refuseAReadingFromTheFuture`, `balance.go:133-141`: "a balance is read off the bank, never projected"), making the asymmetry a clear gap rather than a principle. Related: transaction dates are likewise only checked against the month's bounds (`validate.go:105-114`), so a line dated in the future — impossible on a real statement, and a sign of broken id/date derivation — is accepted without a word.

### M-17 — `move_planned_expense` requires a `base_sha` that no read surface provides, and the contract doesn't say where to get it
**Location:** `internal/api/moveexpense.go:68-74`, `docs/HERMES.md:126-129`

`get_budget`/`list_budget_categories` return no sha (`service.go:81-87`), HERMES.md step 9 says the move "needs a reason" and never mentions `base_sha` at all, and the only discovery path is to provoke the 409 and read `details.current_sha` — meaning every first attempt at this tool fails by design. The MCP schema marks `base_sha` required (`mcp.go:177`, no `omitempty`), so an agent following HERMES.md literally cannot construct a valid first call. Either expose the sha on a read (as `get_actuals` does for actuals via `ActualsMonth.SHA`) or document the conflict-and-retry loop.

---

## Low

### Invoicing core

**L-1 — `pocket-cfo-ctl validate` never runs `money.Compute`, so §4.3's "running total never goes negative" is not a validate-time check.** `cmd/pocket-cfo-ctl/validate.go:33-36`. Validation is unmarshal-only plus `ValidatePaid`; the negative-net guard lives in `money.Compute` (money.go:81), which only fires later in `render` (`render.go:191`) or at request time. A below-zero invoice passes the pre-commit hook and `validate`, failing one pipeline step later.

**L-2 — Silent int64 overflow via `IntPart()` on extreme but schema-valid values.** `internal/money/money.go:51` and `:145-147`; unbounded fields at `schemas/invoice.json:123,125`. `quantity` and `unit_price` have no `maximum`; their product is computed in big.Int and then truncated with `Round(0).IntPart()`, and `big.Int.Int64()` is undefined (wraps) past int64. `quantity = unit_price = 9.2×10^18` unmarshals fine and yields a garbage wrapped line amount with no error.

**L-3 — `unit_price` has no `minimum`, so negative line amounts validate even though credit notes are deliberately deferred (§3.7).** `schemas/invoice.json:125`. A negative-price line is always a typo today, yet it validates and computes silently; only a fully-negative invoice trips the `net < 0` guard. It is also half of the trigger for H-1's panic.

**L-4 — `LoadPaid` silently keeps the last duplicate invoice number.** `internal/stats/paid.go:31-33`. Duplicates in `paid-invoices.json` overwrite earlier entries in the map with no error; `ValidatePaid` catches this in CI, but the webapp path (`LoadPaid`) never runs it, so a duplicate that reaches the data repo renders with whichever date came last.

**L-5 — The overdue boundary flips at UTC midnight, not issuer-local midnight.** `internal/stats/stats.go:100-101` and `:93`. `today` is truncated in UTC; for the Bulgarian issuer (UTC+2/+3) an invoice due today becomes "overdue" at 02:00/03:00 local time. §3.8 says only `due_date < today`; if "today" means the business's local day, the derived state is wrong for a few hours each night.

**L-6 — An invoice whose snapshot recipient number matches no recipient file vanishes from the recipient ledger without any error.** `internal/stats/stats.go:150-160`. `aggregateRecipientRows` iterates recipient *files* and skips numbers with no matching file, so such an invoice appears in `invoiceRows` but contributes to no `TotalInFrame`/`Outstanding`, and no validator cross-checks `recipient.number` references. The two ledgers silently disagree.

### Render pipeline & CLI

**L-7 — `actuals status` rounds negative amounts toward zero.** `cmd/pocket-cfo-ctl/actuals.go:183`. `int(p.Amount*100 + 0.5)` truncates `-12344.49…` to `-12344` instead of `-12345`, so categorized refunds/credits display a cent off; the sibling `roundCents` (`internal/finance/actualsdata/validate.go:214-219`) already handles negatives correctly. Display-only.

**L-8 — `removeStaleDraftPDF` deletes the file but leaves its manifest entry.** `cmd/pocket-cfo-ctl/render.go:239-256`. Every draft→issued transition strands an `INV-…-DRAFT.pdf` key in `render-manifest.json`; `prune` only cleans entries for files it removes itself, so the committed manifest grows dead entries forever.

**L-9 — Missing/corrupt `budget.json` silently disables unknown-category checks in `actuals validate`.** `cmd/pocket-cfo-ctl/actuals.go:266-282` + `internal/finance/actualsdata/validate.go:92,136`. `budgetCategoryIDs` swallows read/parse errors into `nil`, and `nil` knownIDs means "skip the check" — standalone `actuals validate` then passes transactions citing nonexistent categories.

**L-10 — `translateOne` rewrites hand-edited invoice JSON via `MarshalIndent`.** `cmd/pocket-cfo-ctl/translate.go:87-93`. Round-tripping through the generated struct reorders keys, drops the human formatting (blank-line sections in `data/invoices/INV-0000000002.json`), and silently deletes any key the struct doesn't know — producing noisy diffs on every run, and data loss if a key is added to data before the schema.

**L-11 — Quantities/percents render with `.` decimals while money uses `,`.** `internal/render/render.go:144-157`. `formatScaledHundredths(13650)` → `"136.5"` on a de/bg document whose money format is frozen as `10 200,00 €`; no test pins the qty/percent format.

**L-12 — `bilingual` inline threshold counts bytes, not runes.** `internal/render/bilingual.go:25`. The Bulgarian secondary is ~2 bytes/rune, so de/bg pairs stack to block layout at roughly half the visible length of latin pairs — inconsistent inline/stacked decisions across languages.

**L-13 — Doc/code mismatches in rendering constants.** `templates/invoice.html.tmpl:7` loads **PT Sans** while §6 prescribes the exact Noto Sans URL (whose tofu warning the doc repeats); `render.go:267` writes `INV-…-DRAFT.pdf` while §5.1 names the artifact `INV-…-draft.pdf` (code is internally consistent on uppercase, but the doc and any external consumer expecting lowercase will disagree on case-sensitive filesystems).

**L-14 — `prune` treats any `os.Stat` error as "JSON is gone".** `cmd/pocket-cfo-ctl/prune.go:40-42`. A permission/IO error on `data/invoices/INV-….json` (not just `IsNotExist`) causes the corresponding draft PDF to be deleted.

**L-15 — `Require` accepts empty-string translations.** `internal/schema/invoice/helpers.go:27-40`. `Get` reports `ok=true` for a non-nil pointer to `""`; the schema's `minLength: 1` is unenforced, so `"bg": ""` passes both `validate` and `validateLocalization` and renders an empty Bulgarian column on the legal document.

**L-16 — `-h` exits 2.** `cmd/pocket-cfo-ctl/render.go:42-44` (and `delete.go:19-21`). `flag.ContinueOnError` returns `flag.ErrHelp` for `-h`, which these paths treat as a generic parse failure; help requests should exit 0.

**L-17 — Throttling is not retried.** `internal/render/api2pdf.go:171-173` treats 429 as a permanent failure (spec only demands 5xx retries, but a bulk render under rate-limit fails the whole run), and `internal/translate/deepl.go:54-56` has no retry at all for 429/456/5xx, so a transient DeepL error fails the invoice.

**L-18 — `render.HTML` re-parses the template from disk on every call.** `internal/render/render.go:43`. `pdfCurrentMap` (`cmd/pocketcfo/main.go:227-243`) calls it per invoice per index-page load, so the page does N template reads+parses plus N logo reads; correctness is unaffected but it's needless work in a hot path.

### Finance tracker

**L-19 — `DoubleMarked` false-positives on two split parts of one transaction.** `internal/finance/tracker/actuals.go:263-267`. The dedup key is `tx.Date|part.Movement|cents(part.Amount)`, so a single statement line split into two parts that both carry the same movement at the same amount (e.g., a 100 line split 50/50, both `owner_draw`) trips `DoubleMarked`. The parts reconcile to the line, so `CrossedCents` is correct and nothing is double-counted, yet the page shows the "counted twice here … unmark the private side" note, which is wrong advice for this shape. Validation (actualsdata/validate.go:155-164) explicitly permits movement markers on split parts, so the file is legal.

**L-20 — `supersedes` can forget a newer publish.** `internal/finance/tracker/committed.go:44-53`. `bytesFor` and `forget` are separate lock acquisitions; if goroutine A reads body v1, goroutine B then publishes v2 (`publish` + `Evict`), and A finds the disk already matches v1, A's `forget(key)` deletes B's v2. The overlay that exists to cover the deploy lag is then gone for v2, and reads serve the stale disk content until the next eviction. Narrow window, but it defeats exactly the race the overlay exists for when two writes land close together.

**L-21 — The global minimal-mode toggle leaks into the roll-forward.** `internal/finance/tracker/budget.go:40-51` with `accounts.go:294`. `monthClose` → `Budget.ForMonth` → `IsMinimal()`, so toggling "Minimal" re-derives every walked month's planned expenses and shifts the carried opening balances presented as bank-derived figures. The toggle is also process-global (pinned by `TestBudgetMinimalToggleFlipsGlobalState`), so one session's what-if view changes every concurrent session's balances.

**L-22 — The spending page swallows accounts and budget errors.** `internal/finance/tracker/spending.go:127-129, 203-209`. `Snapshot`'s error is discarded (`serr == nil && ok`), and `Budget.ForMonth`'s error is discarded (`if built, berr := …; berr == nil`). A broken `accounts.json` or `budget.json` renders the reconciliation page with no balances and no indication why — the one page where the read date is documented to change what the user does. The dashboard surfaces `AccountsErr`; the spending page says nothing.

**L-23 — The director's loan block shows a 0 net-income row when the cascade failed.** `internal/finance/tracker/view.go:640-657`. `computeDirectorLoan` copies `f.FundingPersonal.NetIncomeCents` without checking `FundingPersonal.Err` (Toggl failure, unrated dividend). The loan block then renders "Net income +0" and a closing figure of `opening − crossed`, while the actual error appears only in the cascade panel above. `publishAccountBalances` (view.go:758) already gates on `FundingPersonal.Err`; the loan block should too.

**L-24 — The mistimed check is year-blind.** `internal/finance/tracker/applyactuals.go:107-125`. `plannedMonth` returns only `time.Month`, and `ChargedMonths` (actuals.go:136-160) scans only the viewed year. A one-off planned for October 2027 but charged in October 2026 compares equal on month alone and reads as on-plan; conversely a charge in November of the *previous* year is invisible to "planned here, already charged elsewhere." One-offs spanning a year boundary escape the mistimed note in both directions.

**L-25 — A transaction with a missing or empty `id` passes validation.** `internal/finance/actualsdata/validate.go:58-76`. `transactionProblems` only rejects *duplicate* ids; a single transaction with no id (or `""`) is accepted, although the schema requires `id` with `minLength: 1` and the id is what makes re-import idempotent. A hand-edited file missing an id loads silently, and a second missing id is then misreported as a "duplicate" rather than as missing.

### Web app & auth

**L-26 — `POST /auth/email` allows sustained mail-bombing of a known allowlisted address.** `cmd/pocketcfo/email_login.go:19, 97-106`. The only throttle is a 60-second in-memory cooldown keyed by email address — no per-IP or global limit, and the map evaporates on restart (the documented deploy model is scale-to-zero, so cold starts reset it). An attacker who knows one allowlisted address can script one real SES email per minute to that victim indefinitely (~1,440/day), incurring SES cost and sender-reputation damage.

**L-27 — Minimal-budget mode is global mutable state shared by all users, and both it and cache eviction are CSRF-able GETs.** `cmd/pocketcfo/finance.go:128-137, 181-190`; `internal/finance/tracker/budget.go:40-45`. `ToggleMinimal` flips `minimalOn` on the single shared `Budget` — one user's `?minimal=toggle` changes what *every* session sees, and there is no per-session scoping. Both it and `?refresh=1` (cache eviction, which forces re-fetching from the Toggl/Holidays APIs and is available to readonly users too) are state changes on GET; `SameSite=Lax` still sends the cookie on cross-site top-level navigations, so a malicious page can trigger them via a link the user clicks.

**L-28 — `-paid.pdf` is served without checking the invoice is still in `paid-invoices.json`; stale paid PDFs are never deleted.** `cmd/pocketcfo/client.go:149-174`, `cmd/pocketcfo/main.go:245-264`; `cmd/pocket-cfo-ctl/render.go:264-280`. ARCHITECTURE.md promises the paid PDF "404[s] until the invoice is listed in paid-invoices.json", but both handlers gate only on file existence (plus issued-status/recipient in the client tier) — the paid list is never consulted. Render targets are write-once and only stale `-DRAFT.pdf`s are cleaned up (`removeStaleDraftPDF`), so if an invoice is paid and later removed from `paid-invoices.json`, the stamped "paid" PDF keeps being served to both the authed tier and the client portal.

**L-29 — Prod accepts an `http://` `PUBLIC_BASE_URL`, silently downgrading cookies and the OAuth redirect to plaintext.** `cmd/pocketcfo/session.go:74-76`, `cmd/pocketcfo/config.go:110-127`. `requireProdVars` only checks `PUBLIC_BASE_URL` is non-empty; `secureCookies()` derives the `Secure` flag from its scheme. `ENV=prod` with `PUBLIC_BASE_URL=http://…` boots cleanly, then issues session/state cookies without `Secure` and uses an `http://` OAuth `redirect_uri` — session hijack / code interception on any network hop. There is also no minimum-entropy check on `SESSION_SECRET`/`OTP_LINK_SECRET`/`CLIENT_LINK_SECRET`.

**L-30 — Internal error strings (absolute filesystem paths) returned to clients.** `cmd/pocketcfo/client.go:66-69, 134-137` (unauthenticated tier), `cmd/pocketcfo/main.go:148-151, 259-261`. `http.Error(w, err.Error(), 500)` on data-load failures surfaces messages like `read /srv/data/recipients: open …: no such file or directory`. In the client-portal handlers the load happens *before* token validation, so even a request with an invalid token receives the path-leaking 500 whenever the data dir is broken.

**L-31 — No security headers on any response; client portal loads third-party CSS.** All HTML handlers; `templates/client.html:8`. No `Content-Security-Policy`, `frame-ancestors`/`X-Frame-Options`, `Referrer-Policy`, or `X-Content-Type-Options` anywhere. html/template's auto-escaping makes XSS unlikely (verified: no `template.HTML` cast touches user-controlled web data — the only casts are constant SVG icons and the CLI-only invoice-logo path), but there is no defense in depth, and the token-bearing portal page pulls `fonts.googleapis.com` CSS with no referrer policy (modern browsers send origin-only cross-origin by default; older ones leak the full token URL as `Referer`).

**L-32 — Readonly session parts are frozen at login for the full 7-day TTL.** `cmd/pocketcfo/email_login.go:62`, `internal/auth/session.go:32-34`. `Parts` are baked into the encrypted cookie and never re-checked against `users.json` per request, so removing a user or revoking a part takes up to 7 days to take effect — even though `users.json` is read fresh from disk on every login-page render and email request anyway, so a per-request re-check would be cheap.

**L-33 — Login link written to logs in plaintext when `SES_FROM_EMAIL` is unset.** `internal/mail/ses.go:21-24`. The full token-bearing URL is logged. In practice unreachable in a meaningful attack (prod requires `SES_FROM_EMAIL`; non-prod bypasses auth entirely), but anyone with log access in a non-prod environment that shares a real `OTP_LINK_SECRET` gets working login links.

**L-34 — `users.json` is loaded — and a failure logged — before input validation on every `POST /auth/email`.** `cmd/pocketcfo/email_login.go:27-29, 88-95`. `emailParts` runs before `validEmail`, so every unauthenticated POST (including garbage) triggers a disk read, and if `users.json` is missing/malformed each request emits a log line — a trivial unauthenticated log-flooding vector.

**L-35 — `Header.Initials()` byte-slices a possibly multibyte login.** `internal/webui/header.go:107`. `login[:1]` splits a multi-byte UTF-8 rune, rendering U+FFFD for logins/emails starting with non-ASCII. Cosmetic only.

### Agent API

**L-36 — An account whose `balances` array is empty can never receive its first reading, and the failure is mislabeled `upstream`.** `internal/api/balance.go:308-310`, `internal/api/balance.go:319-336`. `afterTheLastReading` returns `insertAt = -1` for an empty array, and `rewriteAccount` then fails with `CodeUpstream` ("has no readings to append to") — an error that blames GitHub for a permanent property of the data. The state is reachable: the schema's `minItems: 1` (`accounts.schema.json:45-50`) is not enforced by `ValidateAccounts` (`internal/finance/accountsdata/validate.go:31-50`), so a hand-declared account with `"balances": []` loads fine, appears in `list_accounts`, and can never be brought up to date through the API.

**L-37 — `get_actuals`'s own description (and HERMES.md) claims hand edits appear only after deploy, but with writes configured it reads the Contents API live.** `internal/api/service.go:220-222,233-250`, `internal/api/mcp.go:217-218`, `docs/HERMES.md:217-220`. When `Store != nil` (the normal production case, since writes need it), `ActualsFor` serves from GitHub, so a hand edit committed to the data repo is visible in `get_actuals` immediately — while `search_transactions` and `get_reconciliation_status` read disk+overlay and genuinely do lag until deploy. The docs assert a uniform deploy lag that doesn't hold, so an agent cross-checking a human's correction against search will see the two surfaces disagree in exactly the window the docs say they agree.

**L-38 — The "edit without month can't see recent adds" warnings are stale: the fallback lookup goes through the overlay funnel and can see them.** `internal/api/edit.go:255-302` (transactionMonths → overlay-aware `TransactionsForMonth`), stale text at `internal/api/edit.go:272-273`, `internal/api/mcp.go:79,283`, `docs/HERMES.md:266-269`. `transactionMonths` reads via `s.Actuals.TransactionsForMonth`, which passes through `justWrote.supersedes` (`internal/finance/tracker/actuals.go:221-243`), so a line added minutes ago on the same instance *is* found, and `planEdit` then reads GitHub where the commit already exists — the whole no-month path works for fresh lines. The docs and the not-found error message all describe pre-overlay behavior; harmless direction (agents pass `month` more than they need to), but it is contract drift in the surface the agent reads first.

**L-39 — `SearchQuery.Years` is unvalidated in the service, allowing an unbounded scan and permanent cache growth via MCP.** `internal/api/service.go:385-410,423-453`, `internal/finance/tracker/actuals.go:212-217`. `scanYears` returns `q.Years` verbatim; the REST adapter bounds years to 1970–9999 (`cmd/pocketcfo/api.go:275-285`) but the MCP adapter applies no check, so `years: [1..9999]` triggers ~96k `fs.ReadFile` calls in one request — and `/mcp` has no request timeout (unlike REST's 30s, `cmd/pocketcfo/api.go:17,38`). Every scanned month, including misses, is retained in `Actuals.cache` for the process lifetime (eviction happens only via single-key `Publish` or a manual web `?refresh`, `cmd/pocketcfo/finance.go:129`), so repeated wide scans grow memory monotonically.

**L-40 — All GitHub 422s are reported as SHA conflicts.** `internal/api/contents.go:118-127`. `Put` maps both 409 and 422 to `CodeConflict` ("changed underneath us; re-read and merge"); a 422 from a non-race cause (e.g., content exceeding the Contents API size limit as a month file grows) sends the agent into a pointless re-read-merge loop instead of telling it the real problem.

**L-41 — The no-month edit fallback only scans a three-year window, and its error claims to have searched everything.** `internal/api/edit.go:284-302`. `transactionMonths` scans `now-1 .. now+1`; an edit naming an id older than that gets "no transaction %s in the months on disk", which overstates the search and doesn't mention the window — the agent is told to pass `month`, which works, but the window itself is undocumented.

---

## Verified clean

### Invoicing core
- `roundHalfAwayFromZero` (money.go:145-147) is correct — shopspring `Round` is half-away-from-zero, applied only at line amount, per-discount, and per-group VAT points, matching §3.4.
- `allocateDiscount`'s last-group-remainder construction keeps discounted bases summing exactly to `net`; no negative `DiscountedBase` constructible for ≤3 groups.
- `deriveState` (stats.go:86-97) matches §3.8 including the `due_date == today ⇒ open` boundary; draft-before-paid ordering is defensively correct.
- `ValidatePaid` (paid.go:37-62) covers all three §4.3 rules and aggregates errors; `SerializableDate` strictly rejects impossible dates; `quantity: 0`, `vat_rate` out of 0–100, and negative `percent` are all rejected at unmarshal (those keywords *are* supported).
- `internal/schema/invoice/helpers.go` and its tests: `Get`/`IsEmpty`/`Require` handle nil fields and the bg-only case correctly.
- Reference-invoice tests (4.600,00 € / 10.200,00 €) exist and match `data/invoices/`; the multi-rate proportional-split test exists and is arithmetically correct.

### Render pipeline & CLI
- Core invariants (write-once originals, draft overwrite, failure isolation, frozen money format) are implemented cleanly and well tested.
- CLI commands are careful about clobbering (extract-paid, budget ids' verify-before-write).

### Finance tracker
- Marginal bands and `grossAffordable` inversion (bands_test.go, including accountant-derived vectors).
- "Closing equals rows above" with and without dividends; "full salary closes at zero/target"; target-as-floor oscillation; unknown-vs-zero balance.
- "Actuals move the roll-forward and nothing else" (byte-identical render test); plan-or-statement wholeness; the three movement sets; funding-shift off-by-one regressions.
- Concurrency handled carefully throughout (single-flight Toggl fetches, mutexed caches, per-request tracker copies).

### Web app & auth
- AES-256-GCM sessions with random nonces; HMAC-SHA256 OTP and portal tokens compared with `hmac.Equal`; correct expiry enforcement.
- Well-guarded `returnto` open-redirect checks; proper path-traversal filtering on the PDF/static handlers; constant-time bearer comparison on the API.
- No `template.HTML` bypasses on web-rendered user data.

### Agent API
- Append-only is enforced both by construction and by per-path self-checks; none of the append-only guarantees are bypassable.
- Auth is timing-safe and total; SHA races surface as conflicts; the test suite is unusually honest about what it proves.

---

## Per-domain assessments

**Invoicing core.** The money/stats core is carefully written — integer-minor-unit discipline holds everywhere, rounding is done once at the documented points, and the derived-state logic matches the architecture exactly. The two real weaknesses are at the boundaries: the JSON schemas lean on keywords (`oneOf`, `const`, `propertyNames`) that the chosen generator silently ignores, and missing `minimum` constraints let negative amounts/prices reach a division-by-zero panic in `allocateDiscount`. Most importantly, the tax half of the domain — the part with the hardest correctness requirement (loud failure on EU consumers) — is entirely unimplemented, so the invoicing side currently has no automated defense against a wrong zero-VAT regime on a hand-edited invoice.

**Render pipeline & CLI.** The render pipeline's core invariants (write-once originals, draft overwrite, failure isolation, frozen money format) are implemented cleanly and well tested, and the CLI commands are careful about clobbering. The two systemic weaknesses are validation and failure-mode direction: `validate` is a shallow shadow of the §4.3 contract it claims to implement (letting schema-illegal and legally-noncompliant invoices sail through CI), and the two newest safety mechanisms — the `--base-ref` destruction guard and the existence-based render skip — both fail *open*, silently protecting nothing when git or the filesystem misbehaves.

**Finance tracker.** An unusually disciplined module: the core money arithmetic (cascade ordering, marginal bands, rounded-cent closings, funding shift, plan-or-statement roll-forward) is correct, internally consistent, and pinned by tests that assert the documented invariants rather than incidental behavior. The one serious defect is the ungated cascade input in `rollForward`, which lets a future company balance size salaries in months before its own anchor and quietly corrupts the director's loan in the most common dual-anchor shape; the remaining findings are validation gaps and bounded edge cases rather than arithmetic errors.

**Web app & auth.** The core cryptographic machinery is solid: AES-256-GCM sessions with random nonces, HMAC-SHA256 OTP and portal tokens compared with `hmac.Equal`, correct expiry enforcement, well-guarded `returnto` open-redirect checks, proper path-traversal filtering on the PDF/static handlers, constant-time bearer comparison on the API, and no `template.HTML` bypasses on web-rendered user data. The two real exposures are architectural rather than cryptographic: the fail-open `ENV != "prod"` auth bypass (H-3), which turns one deployment typo into total disclosure including the client-portal bearer secrets, and the synchronous SES send that reintroduces the email-enumeration side channel the design explicitly claims to close (M-10). Everything else is hardening: server timeouts, throttling, paid-PDF lifecycle enforcement, and security headers.

**Agent API.** The write surface is well-defended on its headline promises: append-only is enforced both by construction and by per-path self-checks, auth is timing-safe and total, SHA races surface as conflicts, and the test suite is unusually honest about what it proves. The sharpest real defect is the unvalidated `movement` enum — an accepted, committed, silently inert financial marker — followed by a cluster of contract drift between HERMES.md, the MCP descriptions, and the REST adapter (dropped filters, undiscoverable `base_sha`, stale deploy-lag claims). None of the append-only guarantees are bypassable, but the actuals self-check's blindness to unknown fields is a latent data-loss path that the byte-level checks in the balance and budget paths show is avoidable.

---

## Remediation plan

1. **Batch 1 — Fail-closed the safety nets** (highest value/effort ratio): schema `minimum`s + `subtotal==0` guard (H-1, M-1, L-3), `ENV=development` explicit opt-in (H-3), `gitShow` stderr discrimination (H-4), `splitFlags` for actuals (M-9), `movement` enum validation (H-6).
2. **Batch 2 — Implement `tax.Resolve` + regime cross-check** (C-1) with the §9 table tests.
3. **Batch 3 — Bring `validate` up to §4.3** (H-2, M-2, L-1, L-15): gapless sequence, filename match, date ordering, mandatory wording, translation completeness, discount rules.
4. **Batch 4 — Render pipeline correctness** (M-5, M-6, M-7, M-8): `outputBinary`, git-timestamp staleness for paid PDFs, atomic writes, logo confinement.
5. **Batch 5 — Finance tracker fixes** (H-5, M-12, M-13) + dual-anchor and open-ended-period tests.
6. **Batch 6 — API contract alignment** (M-14, M-15, M-16, M-17) + web hardening (M-10, M-11).
7. **Test gaps** (any batch): stacked-10%+10% discount test (H-7), per-group vs per-line VAT rounding test (M-3).
</content>


---

## Verification notes

Added after the review was worked through. Each finding was re-checked against the code
before anything was changed, and not all of them held.

**Wrong as written**

- **H-2 / M-2 — the generator is less blind than claimed.** go-jsonschema v0.24.1 *does*
  emit `pattern`, `enum`, `minLength`, `minimum`/`maximum` and string `const` checks, so
  `"number": "GARBAGE"`, `"status": "issuedd"`, `"currency": "eor"` and `"vat_rate": 250`
  already failed. The genuinely unenforced set is `additionalProperties: false`,
  `oneOf`/`not`, `propertyNames`, `uniqueItems`, and a non-string `const`
  (`schema_version`). The review contradicts itself here — its own "Verified clean"
  section says those keywords *are* supported. The §4.3 *business-rule* gaps were all
  real, and are now implemented.
- **H-2 — `validate` was not "CI step 1".** It was in no workflow at all, which made the
  gap larger than described rather than smaller. It runs in `build.yml` now.
- **H-6 — the central claim is refuted.** `Movement.UnmarshalJSON` enforces the enum, so a
  typo'd marker could not be committed through any JSON path and was never "silently
  inert". The real residue was one raw cast in the MCP adapter, and its effect was a hard
  parse failure poisoning the month for every reader — the opposite of silent. Fixed at
  the validator, which is the shared gate.
- **M-4 — one cited call site was already correct.** `cmd/pocketcfo/finance.go` skips
  uncomputable invoices rather than propagating.
- **M-9 — the prescribed fix would have broken the flag.** `splitFlags` is value-blind, so
  applying it verbatim would have sorted `HEAD` into the positionals and left
  `--base-ref` with no value. A value-aware split was needed.
- **L-15 — invalid.** `minLength: 1` *is* enforced at unmarshal, so `"bg": ""` never
  reaches the template.
- **L-35 — invalid.** The byte-slice in `Header.Initials` is reachable only when every
  rune is one of five ASCII separators; every other branch already uses `[]rune`.

**Acted on, and wrong**

- **M-5 — the fix broke rendering, and the finding's premise was only half true.** Sending
  `outputBinary: true` is harmless, but api2pdf *ignores* it on the Chrome HTML endpoint and
  still answers with its JSON envelope. v0.26.0 required a `%PDF-` body and rejected the
  envelope, so every render failed the first time it met the real service — caught only
  because `--base-ref`/`fetch-depth: 0` finally made the paid-PDF re-stamp path execute.
  Fixed in v0.27.1 by handling both shapes. The finding is not wrong that the file store is
  an exposure; it is wrong that the flag avoids it.

  The deeper lesson is about the test, not the code: the two-hop test that encoded the real
  contract was *replaced* by one asserting the flag was sent and returning a fake `%PDF-`
  body. A mock rewritten to agree with the change under test cannot contradict it, which is
  the only thing a mock of a third party is for.

**Right, with a wrong detail**

- **H-5** — real, and worse than described. With the loan anchored at 2025-12-31 and the
  bank at 2026-07-31, the director's loan closed **38 489 euros** too high. The
  parenthetical about the fixtures is wrong (`directorloan_test.go` anchors the loan
  *later* in one case), but the conclusion held: no test covered this ordering.
- **§9's shallow-clone prediction is wrong**, which surfaced while implementing M-6. A
  `--depth 1` clone does not return empty timestamps; it attributes every file to the one
  grafted commit, so all timestamps are identical and nothing ever looks newer than
  anything. The failure is quieter than predicted, not louder. `render` now refuses to
  reason from a shallow clone at all.
- **M-10** — real, though the 60-second cooldown narrows it to one probe per address per
  minute, and `users.Load` runs on both branches, so the timing gap is smaller than
  "~1 ms vs 100–500 ms".
- **L-36** — real but reachable only by hand-editing the data repo, since the generated
  unmarshaler rejects an empty `balances` array on load. That path exists precisely
  because `validate` was not in CI.
- **L-13** — the doc contradicts *itself* on the draft filename (`-DRAFT.pdf` in two
  places, `-draft.pdf` in one); the code is consistent. The doc was corrected, and the
  font mismatch was resolved in favour of what actually ships.

**Deliberately not done**

- **The catalog has no `domestic_standard` entry.** The catalog check is scoped to regimes
  invoices actually use. The wording has to come from the accountant before the first
  BG-domestic invoice can be issued; it is not something to invent here.
