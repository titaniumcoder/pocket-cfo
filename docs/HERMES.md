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
   overriding it. Read it so you know what is already recorded and send only what is
   missing. A month that has never been reconciled comes back `not_found`. There is no
   `sha` to keep: neither actuals write takes a `base_sha`, because neither replaces the
   file. (`move_planned_expense` still does — it rewrites `budget.json`, which is a
   different kind of edit.)

3. For each statement line you can't place immediately, **`search_transactions`** on the
   description. A past match with an assigned category is a strong answer; no match means
   ask the user — or record it as `untracked` and come back to it. This is deliberately
   grounded in committed history rather than a rules file, which would drift out of sync
   with what was actually recorded.

4. **Every line gets exactly one of `category`, `ignored`, `untracked` or `splits`.** A
   salary credit or a transfer between the user's own accounts is not a budget expense —
   record it with an `ignored` reason rather than dropping it, so the file still
   reconciles line-for-line against the statement. Never guess, never drop a line.

   **`untracked` is for money you cannot place *yet*** — cash out of a machine being the
   usual case. It takes a note, not a bare yes: `"ATM withdrawal, cash not spent yet"`. It
   is not the same as `ignored`, which is the decision that something is not a budget
   expense; `untracked` is the decision to decide later. Reach for it rather than guessing
   a category or stopping the whole reconciliation on one question. Untracked money is
   excluded from every figure while it sits there, shows up prominently on the spending
   page, and marks the month until it is resolved — nobody forgets about it, which is what
   makes parking it honest rather than lazy. `search_transactions` with `only_untracked`
   lists everything still outstanding.

   **`splits` is for one line that paid for more than one thing** — a cash withdrawal, a
   supermarket run that was half groceries and half hardware, a card settlement covering
   two purchases. The line stays one row and the parts go inside it:

   ```json
   { "id": "f4c8a012", "date": "2026-08-14", "description": "ATM WITHDRAWAL",
     "amount": 100, "account": "Private Checking",
     "splits": [
       { "amount": 50, "category": "<restaurants>" },
       { "amount": 30, "category": "<clothes>" },
       { "amount": 20, "untracked": "still in my wallet" }
     ] }
   ```

   Note the last part. Money still in a wallet has not been spent on anything yet, so it
   is `untracked`, not `ignored` — `ignored` would be claiming it is not an expense, which
   is a different and probably false statement.

   Each part follows the same rule as a line: exactly one of `category`, `ignored` or
   `untracked`, and never an amount of 0. **The parts must add up to the line's `amount`**
   — a split that does not reconcile moves money that nothing else would catch, and
   validation refuses it. Two parts minimum; one part is just a category. Do not split on
   a guess: if you only know where some of a withdrawal went, say exactly that — 60 to
   groceries and 40 `untracked` — rather than inventing the rest.

5. If the answer is "this needs a new budget category", **ask the user**. Hermes does not
   create categories.

6. Derive `id` deterministically — a short hash of account + date + amount + description,
   with a `-2` suffix on collision — so a re-uploaded statement produces identical ids and
   importing twice is idempotent. This is what makes step 7 safe to repeat.

7. **`add_transactions`** records the lines that are not in the file yet. Send only those:
   it never touches, reorders or removes anything already recorded, so there is no
   whole-month document to assemble and nothing is lost by omission.

   **Do not send a month.** Each line's `date` decides which month it is filed under, so a
   statement crossing a month boundary is one call that writes two files. Send `coverage`
   for any month that has never been reconciled — without it, that month cannot be created.

   A line whose `id` is already recorded and identical is skipped and reported back, so
   re-sending a statement is safe and commits nothing. A line whose `id` is already
   recorded but *differs* is refused: that is a change, and changes go through step 8.

8. **`edit_transactions`** changes what recorded lines are attributed to — the common
   correction, and a batch: every Parkmart line in August to Groceries is one call and one
   commit. Each edit names an `id` and exactly one of `category`, `ignored`, `untracked` or
   `splits`, which **replaces** whatever that line carried before. Pass `month` with each
   edit; `get_actuals` and `search_transactions` both return it beside the id.

   It cannot change a `date`, `amount`, `description` or `account` — those are what the
   bank said — and it cannot add or remove a line. Anything you leave out of the array is
   untouched. If any id does not exist, nothing in that month is written and the missing
   ids come back.

9. **`get_reconciliation_status`** reports how much is still `untracked` and any one-off
   charged in a month other than the one it is budgeted for. When it reports a mistimed
   charge, **`move_planned_expense`** shifts the plan to match — it changes that category's
   date and nothing else, and needs a reason.

## There is no removal

Nothing in this API deletes a recorded transaction. `add_transactions` only appends,
`edit_transactions` only re-attributes lines it was given by id, and both check their own
output before committing rather than trusting themselves not to. There is no flag, no
override and no reason that unlocks it.

So a line recorded by mistake is not your problem to undo. Say so, and leave it: a wrong
line that is visible is worth more than a clean file that quietly lost something. A real
repair is a human editing the data repo, where the change gets reviewed like any other.

Coverage follows the same rule — it can be extended but never shortened, because claiming
to have read fewer days than are recorded would reopen a month the dashboard has already
stopped withholding judgement on.

## Rules that are enforced, not advisory

- `coverage` is required and non-empty. Report what you actually read: until it spans the
  whole month the dashboard withholds any under-budget judgement, because a half-read
  month is trivially "under". One range per month — a range may not cross a boundary.
- `amount` is euros, **positive = money out**. A refund is a negative amount against the
  same category. Zero is refused — a line that isn't an expense is `ignored`.
- Every `date` must be a real date and fall inside the month it is filed under.
- A `category` that doesn't resolve is refused before anything is read or committed.
- No line may be left undecided. `untracked` counts as decided; blank does not.

## What a write actually does

Each accepted `add_transactions`, `edit_transactions` or `move_planned_expense` is a **git
commit** to the data repo, which redeploys the app — one commit per month touched. That is
what makes handing an agent an API tolerable: it isn't trusted, it's audited. Every change
is one you can read in `git log` and revert.

The diffs are meant to be readable, which is why new lines go at the end rather than being
sorted into place: a commit from `add_transactions` shows the lines it added and nothing
else, so "it touched nothing that was already there" is something you can confirm at a
glance instead of taking on faith.

The app never writes its own checkout — that directory is ephemeral and would diverge
from git. One consequence worth knowing: the copy `edit_transactions` searches when you
omit `month` is that checkout, so it lags by a deploy and cannot see a line you added a
minute ago. Pass `month` and it never has to look.
