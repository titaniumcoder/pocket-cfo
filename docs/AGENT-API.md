# The agent API

How a read-only AI client — an agent, a script, an assistant — talks to PocketCFO. Most
of the contract lives in the MCP tool descriptions, where the model actually reads it;
this file is the workflow around them.

Both surfaces expose the same service, so they never disagree:

- **MCP** at `/mcp` — typed tools with schemas. Preferred.
- **REST** under `/api/` — the same operations for anything scripted.

Both need `Authorization: Bearer $AGENT_API_TOKEN`. Without that variable set the routes
are not registered at all: an unauthenticated caller gets a 404 and learns nothing, not
even that the surface exists.

**The surface is read-only.** There are no write tools, and there never will be ones
behind a different token: data changes happen in the data repo, committed via git, by a
person. An agent that wants something changed says where the change belongs and stops.

The in-app **Chat** tab (ARCHITECTURE.md §12) asks the same tools the same questions and
has no write path at all.

## The reads

1. **`list_budget_categories`** — that list is the only legal set of `category` values a
   statement line can carry. Never invent an id, and never parse `budget.json` yourself.
   A category that carries a `date` is a one-off counted only in that month; one carrying
   `from`/`until` recurs only inside that window (either bound optional) and is not a
   one-off; one carrying `amount_changes` steps its recurring price at the listed months,
   and the whole scheduled list is reported.

2. **`get_budget`** (or `GET /api/budget/{period}`) — the plan for `YYYY-MM` or `YYYY`,
   with overrides already applied: each category's planned cents, and for a month the
   totals and budget.json's sha. Read these figures rather than recomputing them from
   rates; the month bucket already reflects a price that steps in.

3. **`get_actuals`** (or `GET /api/actuals/{month}`) — the committed month file, read
   straight from the data repo: every statement line with its id, date, description,
   amount, account and disposition. This file is the source of truth; where it disagrees
   with a recollection, it wins. `not_found` means the month was never reconciled.

4. **`search_transactions`** (or `GET /api/transactions`) — text, category, account and
   date-range search across the committed months, returning the month and id beside each
   hit. `include_ignored` pulls the not-an-expense lines in; `only_untracked` lists
   everything still waiting on a decision. A past match is a strong answer about where a
   merchant's money goes.

5. **`list_accounts`** (or `GET /api/accounts`) — every account with the kind of money it
   holds (company or private) and `as_of`, the date it was last read off the bank. An
   `as_of` older than the month that just closed means that account is due a fresh
   reading.

6. **`list_invoices`** (or `GET /api/invoices?year=YYYY`) — every invoice with its
   number, title, recipient, dates, total and state (`draft`, `issued`, `overdue`, or
   `paid` with the date), and the PDFs its state can serve. `years` lists the years
   invoices exist for. The numbers it lists are the only ones that exist.

## Invoices

`GET /api/invoices/{number}/pdf?variant=…` serves the rendered PDF — `draft`, `original`
or `paid` according to the invoice's state — and is REST/browser only, never through MCP.
The states are facts in the data repo: `paid-invoices.json` records payment dates, and
the invoice documents themselves live under `data/invoices/`.

## Balances close a month, and never sit in the middle of one

`accounts.json` readings carry an `as_of` that **is the last day of a month, and nothing
else.** It is that month's *closing* balance — `2026-07-31` is the money left when July
ended, and it is the figure July's page shows beside its Available to spend. A mid-month
date is refused wherever the data is validated, hand edit or not: half a month is not a
closing figure, and a month that has not ended cannot have one at all.

## Where changes happen

Every data file — `budget.json`, `accounts.json`, `data/actuals/*.json`, the invoices —
is hand-maintained in the data repo and every change is a git commit there. `pocket-cfo-ctl
validate` checks all of them before you push, and the app reads whatever is committed.
Nothing in the agent surface stages, proposes or commits a change; an agent asked for one
names the file and the edit and leaves the committing to the person.