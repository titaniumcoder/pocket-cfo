# Changelog

All notable changes to PocketCFO are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and from 1.0.0 on this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Before 1.0.0 a minor bump was used for breaking changes as well; those are marked **Breaking** below.

## [Unreleased]

## [4.6.0] - 2026-10-13

### Added
- **An invoice can carry a `tracking_until` date.** The optional field names the day the invoice's coverage of Toggl trackings ends, up to and including it — the clean separation for clients charged more often than monthly. Absent means the invoice covers everything up to the end of its issue month. `pocket-cfo-ctl validate` accepts it as a date.
- **`togglIgnoredClientIds` in config.json.** A Toggl client id on the list — the own company's, or any client that will never be invoiced — has its hours excluded from income and estimates.
- **The dashboard shows the estimates and the linkage.** The company-income rows carry the estimated invoices as grey rows — "estimate for October 2026, income this month, until the invoice arrives" — with no PDF link, next to the real ones. The tracked-work rows mark a project whose Toggl client no recipient links ("no recipient linked") or that is on the ignore list ("not invoiced"), and the panel's Income total no longer counts work an issued invoice already covers.

### Changed
- **Company income estimates the months no invoice covers yet.** A worked month whose invoice does not exist yet contributes an estimated invoice to the salary cascade — the tracked hours plus the expected workdays the client's invoices do not already cover, landing as income two months after the work, where the real invoice will land once the hours are in. With Toggl unreachable the estimates drop and the real invoices alone remain, with the usual error note.

## [4.5.0] - 2026-10-13

### Removed
- **The in-app chat is gone.** It returns later as a standalone app; until then
  `/chat` is a 404, the menu no longer offers it, and the `openai-go` dependency
  moved with the model client rather than leaving with it. `CHAT_DIR` is no longer
  read; existing chats under the old directory are simply abandoned, and `/info` no
  longer counts or purges them.

### Added
- **An uploads page (`/uploads`, administrators only)** — send in several CSV exports,
  PDF statements or photos of payment confirmations at once; each is stored in an inbox
  under `<STATE_DIR>/uploads` (or `UPLOADS_DIR`) and parsed in the background by an
  OpenAI-compatible model (the chat's client, now in `internal/llm`, configured with the
  same `OPENAI_*` variables). Lines the model cannot classify are staged as untracked;
  the page renders every staged transaction with a category dropdown before anything is
  written, and confirming merges them into `data/actuals/<month>.json` in the data repo
  via the GitHub Contents API (needs `GITHUB_DATA_TOKEN`), deduplicating by the schema's
  deterministic account+date+amount+description id so re-uploading the same statement
  changes nothing. A statement spanning a month boundary commits as two month files.
  `/info` counts the inbox and can purge it.

### Fixed
- **The Last entry table on the spending page starts each account at the beginning of the line again.** The section borrowed the coverage grid's four-column tracks with only two cells per row, so CSS auto-placement put the first account into the header row's third column and the rest drifted from there. It now declares its own two-column layout, and on a phone the date — the one thing the table exists to show — stays visible beside its account instead of the accounts scattering across both columns.

## [4.4.1] - 2026-10-13

### Fixed
- **Issued invoices stay current across template changes.** The staleness badge re-derived each issued invoice's HTML from the *current* template and compared it to a hash recorded when the PDF was rendered — so every template release flagged every issued invoice stale forever, with no way back that didn't violate write-once. Issued PDFs are now pinned in the manifest to their own bytes (an immutability check: the badge turns stale only if the stored PDF file itself changes); drafts keep the HTML comparison, since they re-render on every run. Manifest entries written by earlier versions load as before and are migrated to PDF pins by the next `pocket-cfo-ctl render`, without touching any PDF.

## [4.4.0] - 2026-10-13

### Changed
- **Column headers repeat on continuation pages again — but only while the table continues.** The header row sits at the top of every page that still carries line items (aligned to the natural column widths), and a page holding only the payment card is headed by the strip alone. The fix behind it: a text line renders as a full-width block between table segments, because paged.js cannot split a table row that carries a colspan — the split failed silently and the whole table jumped to the next page.
- **The invoicing page's recipient table sorts case-insensitively by name** ("acme GmbH" beside "Acme AG", not after every capital letter), with the recipient number as tie-break.

## [4.3.0] - 2026-10-13

### Added
- **Lines can be pure text.** `"type": "text"` on a line renders its description as prose spanning the whole table width — an explanation or a headline between the billed positions — and it carries no amounts at all: quantity, unit, unit_price and vat_rate must not be set on one (`pocket-cfo-ctl validate` refuses it). It keeps its place in the line order and contributes nothing to the totals.

### Changed
- **Continuation pages breathe.** The running header on page 2+ is the strip only — the column-header row that shared it is gone, so the strip sits ~1cm from the paper edge with a clear margin below it, and a final page that holds only the payment card is no longer topped by column labels for a table that has ended. The line-items table is back to its natural column widths.

## [4.2.0] - 2026-10-13

### Added
- **Invoices print a letterhead on continuation pages.** Page 2 and onward of a PDF carry a slim running header — logo, issuer, invoice number — above the repeated column headers, and every page gets real top and bottom margins (previously continuation pages ran into the paper edge). The pagination is done by paged.js; if its script cannot load, printing falls back to native Chrome with the same margins and no strip.

## [4.1.0] - 2026-10-13

### Added
- **The month an invoice counts as income can be written down per invoice.** `data/usable-invoices.json` overrides the default (the month after the invoice fell due) — an early-paying client, a December invoice booked in January. Only written-down overrides live there; the file staying empty is the ordinary case, and `pocket-cfo-ctl validate` refuses an override for a draft, an unknown number, or a month before the invoice was due.
- **Invoice line descriptions take a small formatting subset.** A newline is a line break, `**bold**` and `*italic*` are emphasis, and consecutive lines starting with `- ` render as a bullet list — in the PDF and the client portal. The text is escaped before the formatting pass, so nothing but those shapes can be produced.

### Changed
- **`HERMES_API_TOKEN` is now `AGENT_API_TOKEN`, and the agent-facing docs are `docs/AGENT-API.md`.** The surface was never Hermes-specific — any AI client speaks it — so the name no longer pretends otherwise. Same contract, same routes, one rename; unset means the routes are not registered, exactly as before.

## [4.0.0] - 2026-10-13

### Changed
- **The chat is a finance Q&A.** Its system prompt is rewritten for the read-only surface: grounded answers from the tools, no statement loop, no staging language, and the Changes rail is gone from the page.
- **Breaking: a month's company income is what its invoices brought in.** Company income for a month is the invoices that fell due the month before — no Toggl estimate, no two-month funding shift, and the "(from …)" and "Income (for …)" period links leave the dashboard. Toggl still tracks the hours themselves; it no longer prices the budget.
- **Breaking: the payroll cascade is month-local.** What a full salary can afford is simply the month's company income minus its company expenses; nothing carried in, and the month closes at zero. A reserve, when wanted, is an ordinary company expense category in budget.json — on need, like any other spend.
- **Breaking: nothing carries forward.** The roll-forward, the private opening balance and the Balance rows are gone. "Available to spend" is the month's net income less its planned private expenses, with the bank's own month-end reading beside it when one is in force; the company balance block leaves the page, and accounts.json readings are shown for their own month only.
- **Breaking: the agent API is read-only.** Every write tool is gone — add_transactions, edit_transactions, record_account_balance, move_planned_expense, schedule_amount_change, save_draft_invoice, issue_invoice, set_invoice_paid and write_data_file — along with five reads that served them (get_reconciliation_status, get_finance_config, get_invoice_document, read_data_file, derive_transaction_ids). Six read tools remain. Data changes happen in the data repo, committed via git; the in-app chat becomes a read-only Q&A, and its staging machinery (pending changes, apply/discard/revert) is removed.
- **Breaking: coverage is gone from actuals.** The `coverage` array leaves the schema, validation, get_actuals and every status that gated on it; the spending page shows only the last entry date per account.

### Removed
- **Breaking: targetBalance is gone.** The config block, the held-at-minimum-until-reached salary logic, and the target rows on /info are removed; a config.json still carrying a `targetBalance` block is refused with a pointer to the change.

## [3.0.0] - 2026-10-13

### Removed
- **Breaking: Dividends are gone, with both of their taxes.** The `dividends` array in `budget.json`, the `companyProfitTax` and `dividendTax` schedules in config.json's legislation, and every dividend row on the dashboard and the agent API are removed — the machinery existed to serve exceptional owner payouts, and the app refocuses on budgeting. A config.json entry still naming either tax is refused with a pointer to the removal; a budget.json carrying `dividends` fails validation.
- **Breaking: Movement markers on statement lines are gone.** `salary_transfer`, `owner_draw`, `dividend_payout`, `owner_contribution`, `corporate_tax` and `dividend_tax` no longer exist: an actuals line carrying a `movement` fails validation, the spending page lists such a line among the ordinary not-budget expenses, `search_transactions` loses `only_movements`, and the "Money movement" row leaves the dashboard. A transfer between the company and its owner is now just an `ignored` line with a reason.
- **Breaking: The director's loan and the company-worth figure are gone.** The `director_loan` block in `accounts.json` (any leftover entry fails validation), the `get_director_loan` tool, the `GET /api/director-loan/{month}` route, the `kind: director_loan` row in `list_accounts` and the whole loan ledger on the dashboard — including "The company is worth" — are removed. What the company holds in the bank is the only company figure again.

  **Upgrade order matters:** clean the data repo first — drop the `dividends` array from budget.json, any `movement` fields from the actuals, the `director_loan` block from accounts.json, and the `companyProfitTax` / `dividendTax` entries from config.json's legislation — then deploy; every validator refuses these rather than ignoring them.

## [2.0.0] - 2026-09-05

Previewed as 2.0.0-rc.1 to 2.0.0-rc.4. The feature that defines 2.0 is the in-app chat: reconciling bank statements in a conversation inside Pocket CFO, with every write staged for approval, instead of driving an external agent against the API.

### Added
- A **Chat** tab, for admins only, where a bank statement is uploaded and reconciled in a conversation with a model behind any OpenAI-compatible endpoint. The conversation fills the screen with the composer pinned at the bottom — a full-width box, an Attach button with removable file chips, Enter to send — and the Changes rail sits beside it on a wide screen and below it on a phone. The transcript tells logs from answers: the model's reasoning, tool calls and results are gray, collapsed lines between your message and the answer, which is a white card with light formatting. Several chats per user, kept on the server so they survive a reload or a redeploy; a chat can be closed, and `/info` counts the stored chats and can delete them all. Present only when `OPENAI_API_KEY` is set.
- Every write the model proposes is **staged**, not committed: the Changes rail shows it as a pending change — a file edit as its diff — and you approve them together, one commit per touched file however many tool calls produced it, or discard one by one. Every applied change has a **Revert** button that commits the previous content back, or removes a file the change created.
- A turn runs on the server until it finishes: the page follows it over a live event stream and picks it up again after a reload or a tab switch, the composer is disabled meanwhile, and the completion is streamed from the provider so a reasoning model may take as long as it needs — only an endpoint that sends nothing for three minutes is given up on.
- The chat asks instead of assuming: an `ask_user` tool ends the turn with a question card — clickable options and, where sensible, a free-text field — and the answer resumes the turn.
- `read_data_file` and `write_data_file` (MCP, `GET`/`PUT /api/files/{name}`, chat) serve `budget.json`, `accounts.json` and `config.json` whole, for what the narrower tools cannot express — a moved dividend, a new category or account, a new dated rule. A write must pass the file's own validation, becomes one commit with the reason as its message, and reports the diff.
- `derive_transaction_ids` (MCP) and `POST /api/actuals/ids` (REST) hand an agent the stable id of every statement line — a short hash of account, date, amount and description, suffixed `-2`, `-3` for identical lines in one call — so ids are computed by the app rather than by the agent, and a re-imported statement still dedups line for line.
- `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL` and `OPENAI_EXTRA_BODY` configure the chat: any OpenAI-compatible endpoint, the model passed through verbatim (an OpenRouter preset works), and a JSON object merged into every request for provider-specific fields such as OpenRouter's zero-data-retention routing. The key alone is enough to boot: an unset model falls back to `gpt-5` (`openai/gpt-5` on OpenRouter) with a log line.
- Release candidates: a tag such as `v2.0.0-rc.1` publishes the image under its own tag and `:next` — never `:latest` — is marked pre-release on GitHub, and does not notify the data repo, so a test deployment can follow `:next` while production stays on stable tags. The `release-it` skill cuts and later promotes them.

### Changed
- `STATE_DIR` is the one writable directory the app keeps between restarts — `cache/` for Toggl, `chat/` for the chats — set to `/var/data/pocketcfo` by the image, so a deployment mounts one volume there and sets nothing else. `TOGGL_CACHE_DIR` stays as an override, so an existing deployment keeps working; `CHAT_DIR` overrides the chat half.

## [1.0.1] - 2026-09-04

### Fixed

- The year view's Income row no longer claims a shifted period such as "March 2026 –
  February 2027". A year's income is January to December of that year; only the month
  pages say which month their income funds.

## [1.0.0] - 2026-09-03

The first stable release. Nothing changes in the application beyond the two points below;
1.0.0 marks the switch to strict semantic versioning: from here a breaking change bumps the
major version.

### Added
- This changelog, covering every release since 0.1.0.

### Removed
- The per-endpoint header log lines introduced in 0.35.3. They established that neither Toggl API sends quota headers; the client still reads them should they ever appear.

## [0.35.3] - 2026-09-03

### Added
- The log notes the first answer of every Toggl endpoint with its status, header names and any quota-, rate-limit-, retry- or service-level-looking header values, to find out whether any call states the hourly quota.

## [0.35.2] - 2026-09-03

### Fixed
- The cache statistics on `/info` state the year in every timestamp.
- A remaining-quota count that arrives without a reset header still counts for the default hour instead of being discarded.
- The quota row on `/info` appears only once Toggl has actually reported a quota or answered 402; Toggl Track never does, so it no longer shows "not reported yet" forever.

## [0.35.1] - 2026-09-03

### Added
- The Toggl cache directory carries a `VERSION` marker. A release whose cache format differs deletes the old cache files there (top-level `.json` files only) on its first start and begins afresh.

## [0.35.0] - 2026-09-03

### Added
- `/info` shows cache statistics per Toggl backend: months cached and stale, the span of their fetch times, entries in memory, serves from cache, fetches and failures, HTTP requests and retries, the last fetch, fetches in flight and breakers open, the hourly quota, and the snapshot file with its size and write time.

## [0.34.0] - 2026-09-03

### Added
- The Toggl cache survives a restart when `TOGGL_CACHE_DIR` is set: months, projects and rate timelines are written to one JSON file per backend and read back on start, so a Fly machine waking from auto-stop serves the last hours at once.
- `/info` can reset the Toggl cache, in memory and on disk (`POST /toggl/reset`, admin-only).
- The Toggl client respects the hourly request quota: a 402 closes the backend until the reset instead of counting as a failure, the last good hours are served meanwhile, and the finance page says when hours refresh again.

### Changed
- Tracked hours are cached per calendar month instead of per year. A fetch covers one contiguous run of stale months in a single request; the background refresh asks only for the current month (and the previous month through the 7th), older months are refetched every 42 hours or on Reload, and a Reload of a month refetches that month alone.
- Month bounds follow the process location, so a day starts at local midnight on both APIs.
- `TOGGL_REFRESH_INTERVAL` no longer needs raising on Toggl's Free plan; the default fits every plan.

## [0.33.0] - 2026-09-03

### Changed
- The web app no longer reads `API2PDF_KEY`; only `pocket-cfo-ctl render` needs it.
- The Toggl 2.0 panel on `/info` stops guessing at the organization and workspace ids and says where to read them.

### Fixed
- The info page no longer quotes Toggl's expected 403 under the organization id, and shows a question mark for an id it cannot find.

## [0.32.4] - 2026-09-03

### Fixed
- One rules card is open at a time on `/info`, reset to the server's choice on load.
- The Toggl 2.0 client asks for 50 entries per page by default.

## [0.32.3] - 2026-09-03

### Fixed
- The Toggl 2.0 client shrinks its page size when Toggl refuses it.

## [0.32.2] - 2026-09-03

### Fixed
- The rules timeline opens only the current card and marks salary and target like the rest.

## [0.32.1] - 2026-09-03

### Fixed
- A Toggl 2.0 key alone boots, and `/info` finds the ids it needs.

## [0.32.0] - 2026-09-03

### Added
- Toggl 2.0 (focus.toggl.com) as a second source of tracked hours, configured with `TOGGL2_API_KEY`, `TOGGL2_ORGANIZATION_ID` and `TOGGL2_WORKSPACE_ID`.
- `TOGGL_MODE` selects Toggl Track, Toggl 2.0 or both; `both` adds the two APIs' hours together for a migration without live sync.
- The Toggl client remembers a rejected key, and the finance page warns when the key is rejected or about to expire (`TOGGL2_API_KEY_EXPIRES_AT`).
- The info page's configuration table names every setting the process reads, and shows a timeline of the dated finance rules.

### Fixed
- An unset `TOGGL_MODE` stays on Toggl Track when both credential sets exist.

## [0.31.2] - 2026-09-02

### Fixed
- A draft upload can never change an invoice's state.

## [0.31.1] - 2026-09-02

### Fixed
- The runtime image ships `catalog/notes.json`.

## [0.31.0] - 2026-09-02

### Added
- Draft invoices through the agent API: save, issue, read the document and download PDFs, as REST routes and as the MCP tools `get_invoice_document`, `save_draft_invoice` and `issue_invoice`.

### Fixed
- Inserting a number member into a created draft includes the quoted key.

## [0.30.0] - 2026-08-31

### Added
- `list_invoices` and `set_invoice_paid` on REST and MCP; recording a payment is idempotent in both directions.

## [0.29.2] - 2026-08-25

### Fixed
- An out-of-window budget category is not shown unless money moves on it.

## [0.29.1] - 2026-08-24

### Fixed
- Hour unit and compact format align in the Expected and Income rows.

## [0.29.0] - 2026-08-24

### Added
- Budget categories can step their amount at given months; a stepped category pays its in-force amount every month and the page points at the month its price moves.
- `schedule_amount_change` rewrites one category's step list, as an MCP tool and a REST route; the read surface carries the whole `amount_changes` list.

## [0.28.1] - 2026-08-24

### Fixed
- GitHub login sessions stay valid for 30 days.

## [0.28.0] - 2026-08-24

### Added
- Budget categories take a from/until window; a recurring category counts only inside it, and an ended or not-yet-started category is shown as such.

### Fixed
- An ended category stays in the year view it contributed to.
- A bounded recurring category is kept out of the one-off paths of the agent API.

## [0.27.2] - 2026-08-18

### Fixed
- The Content Security Policy blocked every script on the page; the scripts moved out of the page.
- Both directions of the director's loan are called a loan.

## [0.27.1] - 2026-08-18

### Fixed
- **Breaking** for the render pipeline: api2pdf ignores `outputBinary`, so the PDF is taken either way.

## [0.27.0] - 2026-08-17

### Added
- The header says which build this is and which data it is reading (`DATA_UPDATED_AT`, `DATA_COMMIT`).

## [0.26.0] - 2026-08-17

The release that worked through an external code review (1 critical, 7 high, 17 medium, 41 low findings; the review and the verdict on it are in `REVIEW.md`).

### Added
- The two parties decide the tax regime, and a consumer has none.
- JSON is checked against the schema itself, not against what the generator emitted.
- The business rules of §4.3 as pure functions, enforced by `pocket-cfo-ctl validate`, which CI now runs.

### Changed
- **Breaking:** there are two environments, `prod` and `development`; anything else refuses to boot.

### Fixed
- A discount only ever takes money off, and a zero subtotal splits nothing.
- A check that could not run says so instead of printing OK; the validators hold the rules the loader happened to catch.
- The rendered PDF comes back in the response, not from a link anyone can follow; a corrected payment date reaches the paid PDF; a logo is an SVG from the static directory and nowhere else.
- A half-edited draft does not take the invoicing page down.
- A month before every reading still has no balance.
- The login form stops answering faster for addresses that exist.
- The REST surface honours the filters `HERMES.md` tells the agent to use.

## [0.25.0] - 2026-08-17

### Fixed
- **Breaking:** a draw is money the owner has, and Available to spend says so.

## [0.24.0] - 2026-08-17

### Added
- The page says what the company is worth; cash and worth are two figures.
- The loan is declared to the agent, and sizes the dividend that clears it.

### Fixed
- **Breaking:** a declared dividend is not cash leaving the company; an owner draw is what takes the money out.

## [0.23.0] - 2026-08-17

### Added
- Company profit tax and dividend tax as dated legislation.
- A dividend can be planned in `budget.json`; it reaches net income and the company's close, and is charged before the salary.
- A statement line can say it moved money between you and the company; marked transfers get their own section.
- `accounts.json` can state what the company owes its owner; the director's loan accrues and is shown after the balance.
- The agent can write the movement marker and read the rates, the dividends and the loan.
- Untracked cash says which account it left.

## [0.22.0] - 2026-08-13

### Added
- Every imported month shows what the bank saw, not just the plan.

### Fixed
- **Breaking:** a balance reading that does not close its month is refused everywhere.

## [0.21.0] - 2026-08-13

### Added
- An account balance can be recorded through the agent API, and only at a month end; a balance just committed answers reads before it deploys.

## [0.20.1] - 2026-08-13

### Changed
- The dashboard shows an account as a name and a figure.

## [0.20.0] - 2026-08-13

### Added
- An account keeps every balance reading, not just the last.
- A closed month rolls forward on the bank, not the plan; the opening balance says where it came from.
- The running month shows what is spent beside what is planned.

## [0.19.0] - 2026-08-13

### Added
- A month can pay a fixed gross salary.
- The company balance carries and funds payroll; `targetBalance` holds the salary back until the company is funded, and the page says so when a target has no balance to measure.
- Untracked cash leads the spending page.

### Changed
- **Breaking:** an account in `accounts.json` says which pot it is, `company` or `private`.

## [0.18.0] - 2026-08-13

### Added
- The finance page says what rate was actually charged, not a blended one.
- A month pays a full salary, the minimum, or none.
- A configurable month where budgeting begins (`startMonth`).
- A committed month reads back through the agent API before the deploy lands.

### Changed
- **Breaking:** an MCP tool result is an object, never a bare array.
- Documentation rewritten around running your own data repo.

### Removed
- **Breaking:** the PDF signing step that never signed anything.

### Fixed
- The year view is legible on a phone.

## [0.17.0] - 2026-08-12

### Added
- Untracked is a third disposition for recorded cash not decided yet; the spending page shows it and marks the months that have it.
- `add_transactions` files each line by its own date; `edit_transactions` re-attributes recorded lines in a batch.

### Removed
- **Breaking:** `put_actuals` is retired; removal is no longer an operation of the agent API.
- The minimum-salary shortfall note.

### Fixed
- A rate nobody wrote down is not a rate.

## [0.16.0] - 2026-08-12

### Changed
- **Breaking:** contributions are per-party marginal bands, and a ceiling is a band.

## [0.15.0] - 2026-08-12

### Added
- A statutory minimum wage, enforced from the month employment began.
- One dated block for every payroll figure a legislature moves.

### Changed
- **Breaking:** every government figure lives in `legislation`, and nowhere else.

### Fixed
- A failed avatar falls back to the initials it was meant to show.

## [0.14.5] - 2026-08-12

### Fixed
- The dashboard ledger declares its columns too; row rules end together; login returns you where you were.

## [0.14.4] - 2026-08-12

### Fixed
- The phone layout rules were dead, and there are fewer of them now.

## [0.14.3] - 2026-08-12

### Fixed
- A phone-shaped spending page, and one less navigation.

## [0.14.2] - 2026-08-12

### Fixed
- Dates read day-first, as they do everywhere else.

## [0.14.1] - 2026-08-12

### Fixed
- The spending page is one grid with declared column widths, so its columns stop moving.

## [0.14.0] - 2026-08-12

### Added
- One statement line can pay for more than one thing.

## [0.13.3] - 2026-08-12

### Fixed
- The copy control never worked, and is now an icon.

## [0.13.2] - 2026-08-12

### Fixed
- The expense ledgers read as expenses again.

## [0.13.1] - 2026-08-12

### Added
- A tolerance band for actual spending; off-plan reads as weight, not colour; unplanned spending always flags, and a group carries its worst row.

## [0.13.0] - 2026-08-12

### Added
- The Hermes agent API: a read service and REST API, a write that commits a reconciled month through the GitHub Contents API, and a fix for a mistimed one-off; the same service exposed as MCP tools at `/mcp`.
- Per-month planned figures from the budget.
- Spending is a page of its own, quieter, with a copy link per transaction; the menu carries the month you are reading.

### Fixed
- Five gaps an external review found in the Hermes API.
- The group header aligns with the rows it totals; print titles for the invoicing and info pages; the copy link says it needs a secure context.

## [0.12.0] - 2026-08-12

### Added
- Actual spending recorded per month (`data/actuals/YYYY-MM.json`), shown beside the plan with mistimed one-offs flagged, an admin-only drill-down behind the figures, and a CLI check that refuses to let recorded spending vanish.

### Changed
- **Breaking:** every budget category has a stable id.
- The budget figures are named what they are: planned, not spent.
- Static files are served from `/static/{file}`, and spending sits under the month.

## [0.11.0] - 2026-08-11

### Added
- Toggl is refreshed in the background instead of on the request; a pending Income panel renders instead of holding the request.
- `data/paid-invoices.json`, and `invoices extract-paid`, the one-time migration off the old shape.

### Changed
- **Breaking:** payment moves out of the invoice into `paid-invoices.json`.
- Schema-generated Go types are no longer checked in; `go generate` produces them.

### Removed
- **Breaking:** annulment, until there is a real one to model.

### Fixed
- The last good Toggl figures are served when a refresh fails; Toggl has its own HTTP client with retries for transient failures; fetches are single-flighted and a failing key backs off.
- The stale badge judges the archived original and skips drafts.
- The `pocket-cfo-ctl` tests pass on Windows.

## [0.10.0] - 2026-08-07

### Added
- Avatar and icon logout, a segmented period toggle, tighter tables on phones.

## [0.9.0] - 2026-08-07

### Changed
- One content width, one header, and a responsive invoice table.

## [0.8.1] - 2026-08-07

### Fixed
- Negative money rows render red instead of green.

## [0.8.0] - 2026-08-07

### Added
- An Available to spend row; the opening balance moves next to net income.

## [0.7.2] - 2026-08-07

### Changed
- A quieter login page: an outlined GitHub button, email login as a link.

## [0.7.1] - 2026-08-07

### Fixed
- The login page never offered email login.

## [0.7.0] - 2026-08-07

### Changed
- Invoices belong to the budget panel, not the Income panel.

## [0.6.2] - 2026-08-07

### Fixed
- A stale balance nags; it no longer refuses to compute.

## [0.6.1] - 2026-08-07

### Added
- A nag when the account balance has not been read in 40 or more days.

## [0.6.0] - 2026-08-07

### Added
- Account balances; Expected is dropped for elapsed months.

### Fixed
- Every numeric api2pdf field is formatted on `/info`, not just the balance.

## [0.5.3] - 2026-08-07

### Added
- Masked configuration on `/info`; logged-out visitors are redirected to login.

## [0.5.2] - 2026-08-07

### Changed
- The site header is one real shared component.

### Fixed
- `pocket-cfo-data` is notified only after the release binaries are uploaded.

## [0.5.1] - 2026-08-07

Re-tag of 0.5.0 with no changes.

## [0.5.0] - 2026-08-07

### Changed
- Finance, Invoicing and Info share one layout.

### Removed
- `API_PASSWORD` and the `GET /api/net-income/...` JSON API.

## [0.4.1] - 2026-08-07

### Fixed
- Email login never worked for finance-only users.

## [0.4.0] - 2026-08-07

### Added
- The admin-only `/info` diagnostics page; the holiday country is configurable.
- Toggl client ids are fetched per project, with Workspaces and Clients diagnostics; invoice numbers link to their PDF in the Income panel.

### Changed
- **Breaking:** `tracking_project_id` becomes `tracking_client_id` in the recipient schema; invoiced income is keyed by Toggl client.
- `cmd/invoicectl` becomes `cmd/pocket-cfo-ctl`; the exposed configuration surface shrinks to `DATA_DIR` and a flat web layout.

### Fixed
- Invoice suppression resolves via the Toggl client, not the raw project id.
- Invoiced income is no longer double-shifted in the funding and Balance calculation.
- Invoices for unlinked recipients are no longer silently dropped.

## [0.3.1] - 2026-08-07

Re-tag of 0.3.0 with no changes.

## [0.3.0] - 2026-08-07

### Added
- A release notifies `pocket-cfo-data`.

### Changed
- Internal restructuring: the view, main and budget code split into named units; consistent error style with JSON error responses; broad test coverage for the web handlers.

## [0.2.0] - 2026-08-07

### Added
- Standalone release binaries, cross-compiled and attached to the GitHub Release.

## [0.1.1] - 2026-08-07

### Changed
- Skills live in `.agents/skills`, with `.claude/skills` a symlink to it.

## [0.1.0] - 2026-08-07

### Added
- PocketCFO: the finance tracker and the invoicing tool as one application, shipped as one Docker image with both executables, `pocketcfo` and `pocket-cfo-ctl`.
- Releases are cut from a human-confirmed proposal by the `release-it` skill instead of release-please.

### Fixed
- A zero-month recurring category previews its next occurrence.
- Leftover Invoicer branding and the `GITHUB_REPO` bug from the merge; the finance tracker's GitHub login link pointed at a nonexistent route.

[Unreleased]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.6.0...HEAD
[4.6.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.5.0...v4.6.0
[4.5.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.4.1...v4.5.0
[4.4.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.4.0...v4.4.1
[4.4.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.3.0...v4.4.0
[4.3.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.2.0...v4.3.0
[4.2.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.1.0...v4.2.0
[4.1.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v4.0.0...v4.1.0
[4.0.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v3.0.0...v4.0.0
[3.0.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v2.0.0...v3.0.0
[2.0.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v1.0.1...v2.0.0
[1.0.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.35.3...v1.0.0
[0.35.3]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.35.2...v0.35.3
[0.35.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.35.1...v0.35.2
[0.35.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.35.0...v0.35.1
[0.35.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.34.0...v0.35.0
[0.34.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.33.0...v0.34.0
[0.33.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.32.4...v0.33.0
[0.32.4]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.32.3...v0.32.4
[0.32.3]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.32.2...v0.32.3
[0.32.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.32.1...v0.32.2
[0.32.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.32.0...v0.32.1
[0.32.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.31.2...v0.32.0
[0.31.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.31.1...v0.31.2
[0.31.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.31.0...v0.31.1
[0.31.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.30.0...v0.31.0
[0.30.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.29.2...v0.30.0
[0.29.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.29.1...v0.29.2
[0.29.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.29.0...v0.29.1
[0.29.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.28.1...v0.29.0
[0.28.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.28.0...v0.28.1
[0.28.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.27.2...v0.28.0
[0.27.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.27.1...v0.27.2
[0.27.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.27.0...v0.27.1
[0.27.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.26.0...v0.27.0
[0.26.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.25.0...v0.26.0
[0.25.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.24.0...v0.25.0
[0.24.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.23.0...v0.24.0
[0.23.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.22.0...v0.23.0
[0.22.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.21.0...v0.22.0
[0.21.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.20.1...v0.21.0
[0.20.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.20.0...v0.20.1
[0.20.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.18.0...v0.19.0
[0.18.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.14.5...v0.15.0
[0.14.5]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.14.4...v0.14.5
[0.14.4]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.14.3...v0.14.4
[0.14.3]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.14.2...v0.14.3
[0.14.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.14.1...v0.14.2
[0.14.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.13.3...v0.14.0
[0.13.3]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.13.2...v0.13.3
[0.13.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.13.1...v0.13.2
[0.13.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.8.1...v0.9.0
[0.8.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.7.2...v0.8.0
[0.7.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.7.1...v0.7.2
[0.7.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.6.2...v0.7.0
[0.6.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.5.3...v0.6.0
[0.5.3]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/titaniumcoder/pocket-cfo/releases/tag/v0.1.0
