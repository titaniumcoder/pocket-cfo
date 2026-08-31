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
   precisely so they survive a category being renamed. A category that carries a `date`
   is a one-off counted only in that month; one carrying `from`/`until` recurs only
   inside that window (either bound optional) and is not a one-off; one carrying
   `amount_changes` steps its recurring price at the listed months, and the whole
   scheduled list is reported so you can see which months are already spoken for.

2. **`get_actuals`** for the month. The committed file is the source of truth. Your own
   memory of recurring merchants is a starting point for *proposing*, never for
   overriding it. Read it so you know what is already recorded and send only what is
   missing. A month that has never been reconciled comes back `not_found`. There is no
   `sha` to keep: neither actuals write takes a `base_sha`, because neither replaces the
   file. (The budget writers — `move_planned_expense` and `schedule_amount_change` — still
   do: they rewrite `budget.json`, which is a different kind of edit.)

3. For each statement line you can't place immediately, **`search_transactions`** on the
   description. A past match with an assigned category is a strong answer; no match means
   ask the user — or record it as `untracked` and come back to it. This is deliberately
   grounded in committed history rather than a rules file, which would drift out of sync
   with what was actually recorded.

4. **Every line gets exactly one of `category`, `ignored`, `untracked` or `splits`.** A
   salary credit or a transfer between the user's own accounts is not a budget expense —
   record it with an `ignored` reason rather than dropping it, so the file still
   reconciles line-for-line against the statement. Never guess, never drop a line.

   **`movement` rides *beside* `ignored`, never instead of it.** It says a line moved money
   between the company and its owner, which is what the director's loan is settled from:
   `salary_transfer`, `owner_draw`, `dividend_payout`, `owner_contribution` cross between
   the two; `corporate_tax` and `dividend_tax` leave the company for the state and cross
   nothing, and are marked only so the spending page can list them. Such a line is still
   not a budget expense, so it still carries a reason.

   A marked line now moves the company's bank figure as well as the loan, so marking
   the wrong side moves two figures rather than one. The two sets are not the same:
   a `salary_transfer` settles the loan but does **not** leave the bank again (the
   cascade already charged the gross salary), and `corporate_tax` / `dividend_tax`
   leave the bank without settling anything.

   **Record it once, on the company statement.** Both statements are imported, so the same
   transfer reaches you twice, and marking both counts it twice. The sign enforces this:
   everything except `owner_contribution` must be money *out* of the company, so the mirror
   line on the private statement is refused with a message saying where the marker belongs.
   Leave that mirror as a plain `ignored` line. `search_transactions` with `only_movements`
   shows what is already marked, which is how you avoid marking the other side by mistake.

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
   `date` and nothing else, never touches anything that recurs monthly or is bounded to a
   `from`/`until` window, and needs a reason and a `base_sha`. That sha is the `sha` field
   `get_budget` returns; sending a stale one comes back as a conflict carrying the current
   value, so you can re-read and try again.

10. **`schedule_amount_change`** steps a recurring price from a future month on — the
    tool for a rent that rises next January. Send `category_id`, `from_month` (YYYY-MM),
    `amount`, and optionally `minimal_amount`, which minimal-budget mode uses for the
    months the change is in force; sending the same `from_month` again corrects the
    scheduled price — the correction replaces that month's entry as a whole, so resend
    `minimal_amount` if it should stay. Amounts are never negative, and `minimal_amount`
    cannot exceed the amount it reduces. Send `remove` instead of `amount` to call a
    scheduled change off.
    Only a future month may be planned: a month that is already in force cannot be
    re-planned here — an already closed budget is fixed in `budget.json`, so say so and
    stop. It refuses one-offs (a single price, full stop) and a change outside the
    category's own `from`/`until` window, which could never take effect. Read the
    category's `amount_changes` from `list_budget_categories` or `get_budget` first — it
    is the whole scheduled list — and the `sha` `get_budget` returns is the `base_sha`;
    a stale one comes back as a conflict carrying the current value, so re-read and try
    again.

11. When a month closes, **`record_account_balance`** for each account the user reads off
    the bank. `list_accounts` shows the `as_of` of the newest reading each one has, so an
    account still sitting on an older month is one to ask about. See below — the date rule
    is not negotiable.

12. **`get_director_loan`** is where the markers land: what the company owes the owner at
    the end of a month, or what the owner owes the company. Read it to check a marker you
    just wrote did what you meant, and read its `notes` — they say when a month is not
    fully imported, so the figure reads high, and when one transfer looks marked twice. A
    month before any figure has been stated answers `known: false`, which means *nobody has
    said*, not *nothing is owed*.

13. **`get_finance_config`** is the dated rules every figure is computed from — both
    contribution schedules, income tax, company profit tax, dividend tax, the salary plan
    and the target balance. Read it before explaining a figure rather than assuming a rate.
    A month before the earliest entry has nothing in force and is charged nothing, which is
    not the same as a rate of zero.

## Invoices

`list_invoices` (or `GET /api/invoices?year=YYYY`) reports every invoice the dashboard
shows — issued and draft alike — with number, title, recipient, issue and due dates, the
total in cents, and a state: `draft`, `issued`, `overdue` (due date passed unpaid), or
`paid` with the date it was paid on. `years` lists the years invoices exist for. The
numbers it lists are the only legal values for `set_invoice_paid`'s `invoice` field.

**`set_invoice_paid`** (or `POST /api/invoices/paid`) is the one invoicing write. It
records a payment in `data/paid-invoices.json` — never inside the invoice document, which
is write-once once issued:

- `paid: true` needs `date`, the day the money arrived, as the bank shows it — never a
  future date, and never the issue date by default. Recording it again with a different
  date corrects the record; `paid: false` removes the entry. Both directions are
  **idempotent**: re-sending an identical payment, or unmarking an invoice that was never
  marked, changes nothing and commits nothing — repeating a call is always safe.
- There is no amount and no account field, by design (ARCHITECTURE.md §3.6): one date per
  invoice. The optional `note` is where a bank reference, or which account the money
  landed on, goes as free text.
- A draft is refused — a draft is never paid — and so is an invoice that does not exist;
  `list_invoices` is the source of numbers.

The dashboard's staleness badge does the rest: listing an invoice in `paid-invoices.json`
renders its `-paid.pdf`, and a corrected date stales the paid copy again.

## What you cannot write, and why

Three things are readable here and only editable in the data repo, deliberately:

- **The rates** (`config.json`). Deployment configuration, changed by redeploying. A past
  payslip has to stay reproducible against the rates it was actually computed under.
- **A dividend** (`budget.json`). A distribution is a decision taken with the accountant,
  and it moves both the company's balance and the owner's income. `get_budget` reports any
  planned for a month with both taxes already worked out, so you never recompute them. The
  one exception is a recurring category's *future* step: `schedule_amount_change` (step 10)
  can schedule, correct, or call off a month's amount — but only in a month that has not
  arrived yet, and never on a one-off, a dividend, or a month that is already in force.
- **The director's loan's opening figure** (`accounts.json`). Unlike a bank balance there
  is nothing to read it off; it is a year-end restatement from the accountant. Appending a
  reading corrects everything after it without rewriting what was true before.
  `list_accounts` reports the loan with `kind: director_loan` so you know it exists — it is
  not a statement account, has no coverage, and `record_account_balance` refuses it. Read it
  with `get_director_loan` and otherwise leave it alone.

One thing worth knowing before you advise on a distribution: **declaring a dividend moves no
money.** It hands the owner a claim, which the director's loan carries; only the two taxes
leave the bank. So a company with almost nothing in the account can still declare the
distribution that clears what its owner already drew — `get_director_loan` sizes it, and
`cash_needed_cents` is the figure that has to be affordable, not the gross.

If the user asks you to change one of these, say where it lives rather than looking for a
tool. There isn't one.

## Balances close a month, and never sit in the middle of one

`as_of` **is the last day of a month, and nothing else.** It is that month's *closing*
balance, read at the end of that day, so it is what the **next** month opens with:
`2026-07-31` is the money left when July ended, it is August's starting figure and usable
in August immediately, and it changes nothing about July.

**A mid-month balance is refused.** Not filed under the month anyway, not rounded to the
nearest month end — refused, with the date it should have carried. The same rule holds for
a hand edit: `accounts.json` fails to load if any reading in it does not close its month,
so this is not a restriction on the API but the shape of the data. Half a month is not a
closing figure: the rest of that month's spending has not happened yet, so recording the
balance on the 15th as the month's figure would open the next month with money that is
already gone. If the user gives you a figure they read mid-month, ask them to read it
again when the month closes rather than sending it. A month that has not ended yet is
refused for the same reason: a balance is read off the bank, never projected.

The reading is **appended** to that account's history — like every other write here,
nothing is written over. Every figure ever read is kept, which is what lets a past month
keep the number that was true then instead of being re-explained by a reading from a year
later. A month that already has a figure comes back as a conflict, with the figure that is
already there; correcting it is a human editing the data repo.

`account` must be spelled exactly as `list_accounts` spells it, and **an account is never
created** by this call. Which pot it belongs to — `company` or `private` — decides which
side of the payroll cascade the money sits on, and that is a decision to be made by a
person, not inferred from a name. An unknown name comes back with the names that do exist.

There is no `base_sha`: the reading is appended, so a change to another account merges
rather than being lost. `balance` is euros at the end of that day, decimals allowed and
negative for an overdrawn account. `note` is optional free text shown beside the account
in the month it anchors.

## When a write becomes visible

Every accepted write is a commit, and the running app is rebuilt from that commit — which
takes a few minutes. In between, the app serves what you just committed from memory, so
**a month you have written reads back immediately**: `get_actuals`,
`get_reconciliation_status` and `search_transactions` all reflect it, and so does the
spending page. Call `get_reconciliation_status` straight after a write to check it landed.
A balance you recorded is in force the same way — `list_accounts` reports the new `as_of`
at once, and the dashboard opens the next month on the new figure. A scheduled amount
change reads back the same way — `get_budget` reports the new step at once, and the
categories page shows it as an arrow on the category.

Two limits worth knowing. A change made somewhere other than this app — a hand edit in the
data repo, or another instance — appears only once it has deployed, *except* through
`get_actuals`, which reads the data repo directly whenever writes are configured and so
sees a hand edit at once. So `get_actuals` and `search_transactions` can disagree for the
length of one deploy, and `get_actuals` is the one that is up to date. And the app never
writes to its own copy on disk: the memory is dropped when the deploy replaces the file, so
git remains the only thing that is true.

## There is no removal

Nothing in this API deletes a recorded transaction. `add_transactions` only appends,
`edit_transactions` only re-attributes lines it was given by id, and both check their own
output before committing rather than trusting themselves not to. There is no flag, no
override and no reason that unlocks it. `record_account_balance` follows the same rule
from the other direction: it appends a reading and refuses a month that already has one,
so no balance ever written is written over.

So a line recorded by mistake is not your problem to undo. Say so, and leave it: a wrong
line that is visible is worth more than a clean file that quietly lost something. A real
repair is a human editing the data repo, where the change gets reviewed like any other.
Calling a *scheduled* amount change off (`schedule_amount_change` with `remove`) is not a
removal in this sense: it only takes down a future step before it has taken effect, and a
month that is already in force it cannot touch at all.

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
- A balance's `as_of` is the **last day of its month**. Any other day is refused, as is a
  month that has not ended yet, and a month that already has a reading.
- A scheduled amount change may only start in a **future month**. A month that is already
  in force is an already closed budget — it is refused, and the answer is to fix
  `budget.json`, not to re-plan it here.
- An account is never created by a write. An unrecognised name is refused with the names
  that do exist.

## What a write actually does

Each accepted `add_transactions`, `edit_transactions`, `move_planned_expense`,
`schedule_amount_change`, `record_account_balance` or `set_invoice_paid` is a **git
commit** to the data repo, which redeploys the app — one commit per month touched. That is
what makes handing an agent an API tolerable: it isn't trusted, it's audited. Every change
is one you can read in `git log` and revert.

The diffs are meant to be readable, which is why new lines go at the end rather than being
sorted into place: a commit from `add_transactions` shows the lines it added and nothing
else, so "it touched nothing that was already there" is something you can confirm at a
glance instead of taking on faith.

The app never writes its own checkout — that directory is ephemeral and would diverge
from git. The lookup `edit_transactions` does when you omit `month` reads through the same
memory as everything else, so it *does* find a line you added a minute ago — but it scans
the months around now, which is slower and only covers a three-year window. Pass `month`
and it never has to look.
