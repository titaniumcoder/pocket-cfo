# Hermes

How the reconciliation agent talks to PocketCFO. Most of the contract lives in the MCP
tool descriptions, where Hermes actually reads it; this file is the workflow around them.

Both surfaces expose the same service, so they never disagree:

- **MCP** at `/mcp` — typed tools with schemas. Preferred.
- **REST** under `/api/` — the same operations for anything scripted.

Both need `Authorization: Bearer $HERMES_API_TOKEN`. Without that variable set the routes
are not registered at all: an unauthenticated caller gets a 404 and learns nothing, not
even that the surface exists. Writes additionally need `GITHUB_DATA_TOKEN`; without it
reads work and writes return `write_not_configured`.

## The loop

1. **`list_budget_categories`** — that list is the only legal set of `category` values.
   Never invent an id, and never parse `budget.json` yourself. The ids are UUIDs
   precisely so they survive a category being renamed.

2. **`get_actuals`** for the month. The committed file is the source of truth. Your own
   memory of recurring merchants is a starting point for *proposing*, never for
   overriding it. Keep the `sha` it returns — that is `base_sha` for the write in step 6.
   A month that has never been reconciled comes back `not_found`, and its `base_sha` is
   the empty string.

3. For each statement line you can't place immediately, **`search_transactions`** on the
   description. A past match with an assigned category is a strong answer; no match means
   ask the user. This is deliberately grounded in committed history rather than a rules
   file, which would drift out of sync with what was actually recorded.

4. **Every line gets exactly one of `category` or `ignored`.** A salary credit or a
   transfer between the user's own accounts is not a budget expense — record it with an
   `ignored` reason rather than dropping it, so the file still reconciles line-for-line
   against the statement. Never guess, never drop a line.

5. If the answer is "this needs a new budget category", **ask the user**. Hermes does not
   create categories.

6. Derive `id` deterministically — a short hash of account + date + amount + description,
   with a `-2` suffix on collision — so a re-uploaded statement produces identical ids and
   importing twice is idempotent.

7. **`put_actuals`** submits the **whole month**, including every transaction already
   recorded. Anything you leave out is a removal and will be refused unless
   `allow_removals` gives a reason, which lands in the commit message.

8. A **conflict** means the file changed underneath you: re-read, merge, resubmit. Never
   retry blind.

9. **`get_reconciliation_status`** reports any one-off charged in a month other than the
   one it is budgeted for. When it does, **`move_planned_expense`** shifts the plan to
   match — it changes that category's date and nothing else, and needs a reason.

## Rules that are enforced, not advisory

- `coverage` is required and non-empty. Report what you actually read: until it spans the
  whole month the dashboard withholds any under-budget judgement, because a half-read
  month is trivially "under".
- `amount` is euros, **positive = money out**. A refund is a negative amount against the
  same category. Zero is refused — a line that isn't an expense is `ignored`.
- Every `date` must fall inside the month. A statement crossing a month boundary splits
  into two files.
- A `category` that doesn't resolve is refused before anything is committed.

## What a write actually does

Each accepted `put_actuals` or `move_planned_expense` is a **git commit** to the data
repo, which redeploys the app. That is what makes handing an agent an API tolerable: it
isn't trusted, it's audited. Every change is one you can read in `git log` and revert.

The app never writes its own checkout — that directory is ephemeral and would diverge
from git.
