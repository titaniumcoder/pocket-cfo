# Changelog

All notable changes to PocketCFO are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and from 1.0.0 on this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Before 1.0.0 a minor bump was used for breaking changes as well; those are marked **Breaking** below.

## [Unreleased]

## [2.0.0-rc.1] - 2026-09-04

A release candidate for 2.0: the in-app chat. Published as `:next`, not `:latest`.

### Added
- A **Chat** tab, for admins only, where a bank statement is uploaded and reconciled in a conversation with a model: read tools run at once, every write the model proposes is staged as a pending change that nothing commits, and the conversation is kept on the server so it survives a reload or a redeploy. Several chats per user; a chat can be closed. Present only when `OPENAI_API_KEY` is set. Pending changes are approved together — one commit per touched file, however many tool calls produced it — or discarded one by one, and every applied change has a **Revert** button that commits the previous content back (or removes a file the change created). `/info` counts the stored chats and can delete them all.
- `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL`, `OPENAI_EXTRA_BODY` and `CHAT_DIR` configure the in-app Chat tab: any OpenAI-compatible endpoint, the model passed through verbatim, and a directory for the chats. The key set without a model or without anywhere to keep chats refuses to boot; `/info` shows the resolved values.
- Release candidates: a tag such as `v2.0.0-rc.1` publishes the image under its own tag and `:next` — never `:latest` — is marked pre-release on GitHub, and does not notify the data repo, so a test deployment can follow `:next` while production stays on stable tags. The `release-it` skill cuts and later promotes them.
- `derive_transaction_ids` (MCP) and `POST /api/actuals/ids` (REST) hand an agent the stable id of every statement line — a short hash of account, date, amount and description, suffixed `-2`, `-3` for identical lines in one call — so ids are computed by the app rather than by the agent, and a re-imported statement still dedups line for line.

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

[Unreleased]: https://github.com/titaniumcoder/pocket-cfo/compare/v2.0.0-rc.1...HEAD
[2.0.0-rc.1]: https://github.com/titaniumcoder/pocket-cfo/compare/v1.0.1...v2.0.0-rc.1
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
