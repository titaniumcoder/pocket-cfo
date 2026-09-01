# PocketCFO — Architecture

Two halves in one binary: **invoicing**, and a **finance tracker**. Sections 1–7 and 10
are the invoicing side, which is the older and more constrained of the two; §8 covers the
web app that serves both; §10 the finance tracker; §11 the agent-facing write API.

**Code and data are separate repos.** This one is public and MIT licensed and carries
fabricated sample data; a private data repo holds one business's real data and bakes it
into the published image. See README.md for setting up the second one.

**One JSON file per invoice, and CI builds the PDFs.** Invoice data is edited by hand and
committed; it is never edited by the app. Finance data has one exception — the agent API
in §10 — and that writes by committing to the data repo, never to its own disk.

Reference documents: `INV-0000000001` (Swiss recipient, non-EU place of supply) and
`INV-0000000002` (Austrian recipient, EU reverse charge). Between them they cover both
zero-VAT branches the system must handle.

---

## 1. Principles

1. **One JSON file per invoice**, containing the document itself and nothing mutable.
   Once issued it is never edited again — payment is recorded separately, in
   `data/paid-invoices.json` (§3.6), so settling an invoice can't churn a file whose
   whole value is that it doesn't change.
2. **Nothing calculable is stored.** No line amounts, no subtotal, no VAT, no total — a
   stored figure is one that can disagree with its own lines.
3. **Two PDFs per invoice, at most**:
   - `INV-0000000002.pdf` — the original. Written once, never overwritten.
   - `INV-0000000002-paid.pdf` — stamped paid. Re-rendered
     when the JSON changes.
   A third, `-ANUL.pdf`, returns with annulment (§3.7), which isn't built.
4. **Tamper evidence is git history**, not a hash field. `git log -p` on one invoice's
   JSON is the audit trail, and the PDF beside it in `build/` is written once and
   committed, so altering either is a commit with an author and a date.
5. **Money is integer minor units.** `1020000` = 10 200,00 €. No floats.
6. **Legal text is accountant-authored, never machine-translated.**
7. **Derived state is computed, not stored** — overdue, outstanding, days to pay.

At 30 invoices a year with one writer there is no concurrency: no locking, no sequence
allocator, no transactions. If something below looks suspiciously simple, that's why.

---

## 2. Layout

```
.github/workflows/
  build.yml          push to main → validate, render, index, commit
cmd/
  pocketcfo/         web app: finance tracker, invoicing viewer, agent API
  pocket-cfo-ctl/    CLI — the whole pipeline, identical locally and in CI
internal/
  api/               the agent-facing service, and its REST and MCP adapters (§11)
  finance/           the finance tracker (§10): tracker, config, and the data schemas
  schema/            generated types for the invoicing documents
  tax/               which VAT regime an invoice falls under (§4)
  money/             totals, discounts, VAT grouping — integer minor units (§3.4)
  render/            invoice HTML → api2pdf → PDF (§6)
  stats/             derived state: which invoices are draft, issued, overdue, paid
  translate/         DeepL, for the Bulgarian half of an invoice
  auth/              sessions, GitHub OAuth, the email login token (§8)
  mail/              Amazon SES, for that login link
  users/             who may read which half, by email (§8)
  webui/             the one site header every page shares
schemas/             invoice.json, recipient.json, issuer.json, notes.json
catalog/notes.json   ← accountant-owned
data/
  issuer.json
  recipients/0007.json
  invoices/INV-0000000001.json      ← everything about this invoice
  invoices/INV-0000000002.json      ← everything about this invoice
build/
  INV-0000000002.pdf
  INV-0000000002-paid.pdf
  index.json                        all invoices, one fetch for the app
templates/
static/
testdata/
```

`data/` and `build/` above are defaults — `DATA_DIR`/`BUILD_DIR` override them,
e.g. to point at a data checkout that lives outside this repo. `templates/`
and `static/` are likewise defaults, overridable via `TEMPLATES_DIR`/
`STATIC_DIR` for a deployment that wants its own branding.

No sequence file: the next number is `max + 1`, and CI checks the sequence is gapless.
No hash sidecars, no meta files, no template versions.

`pocket-cfo-ctl` does all the real work, so the whole pipeline runs locally with one command.
Debugging a render by pushing commits to CI is misery; don't design yourself into it.

---

## 3. Data model

### 3.1 Issuer — `data/issuer.json`

Legal name, address, TIN, VAT ID, bank block (name, IBAN, BIC, optional intermediary
BIC), logo, default currency. Example shape:

```json
{
  "legal_name": "Example Issuer EOOD",
  "address": { "line1": "Musterstraße 1", "postal_code": "9000", "city": "Varna", "country_code": "BG" },
  "tax_id": "000000000",
  "vat_id": "BG000000000",
  "bank": { "name": "Example Bank", "iban": "DE89 3704 0044 0532 0130 00", "bic": "COBADEFFXXX" },
  "logo": "static/logo.svg",
  "default_currency": "EUR"
}
```

### 3.2 Recipient — `data/recipients/0007.json`

```json
{
  "number": 7,
  "legal_name": "Musterfirma GmbH",
  "address": { 
    "line1": "Musterstraße 1", 
    "postal_code": "1010",
    "city": "Wien", 
    "country_code": "AT" 
  },
  "tax_id": "ATU00000000",
  "vat_id": "ATU00000000",
  "is_business": true,
  "language": "de",
  "payment_terms_days": 30,
  "email": "buchhaltung@musterfirma.example"
}
```

### 3.3 Invoice — `data/invoices/INV-0000000002.json`

```json
{
  "schema_version": 1,
  "number": "INV-0000000002",
  "status": "issued",

  "type": "invoice",
  "title": "Example Project / January 2026",
  "issue_date": "2026-01-15",
  "due_date": "2026-01-22",
  "currency": "EUR",
  "language": "de",

  "issuer":    { "…snapshot of issuer.json…" },
  "recipient": { "…snapshot of recipients/0007.json…" },

  "lines": [{
    "description": {
      "de": "Beispielprojekt - Betrieb und Weiterentwicklung - Planung, Betrieb und Weiterentwicklung",
      "bg": "Примерен проект – Експлоатация и по-нататъшно развитие – Планиране, експлоатация и по-нататъшно развитие"
    },
    "quantity": 13600, "unit": "hrs",
    "unit_price": 7500,
    "vat_rate": 0
  }],

  "discounts": [
    { "label": { "de": "Skonto", "bg": "Отстъпка" }, "percent": "2" }
  ],

  "tax": {
    "regime": "eu_b2b_reverse_charge",
    "citations": ["ЗДДС чл. 21 ал. 2", "ЗДДС чл. 86 ал. 3"],
    "note": {
      "de": "Steuerschuldnerschaft des Leistungsempfängers (Reverse Charge). Keine Umsatzsteuer gemäß Art. 21 Abs. 2 und Art. 86 Abs. 3 ЗДДС.",
      "bg": "Данъкът не е начислен на основание чл. 21, ал. 2 и чл. 86, ал. 3 ЗДДС. Данъкът е изискуем от получателя (обратно начисляване)."
    }
  }
}
```

Flat, no nesting, **and nothing that can be calculated is stored**. No line `amount`, no
`subtotal`, no `vat`, no `grand_total`. A stored total is a total that can disagree with
its own lines, and hand-editing makes that a matter of time.

**`status` is the only field that matters for the build**: `draft` renders to a -DRAFT.pdf which will be 
overwritten in each build,
`issued` renders and the issued PDF will never be overwritten by the build

**The recipient and the legal note are snapshotted, not referenced.** Editing a client's
address or rewording a note in the catalog must not alter a document you already sent. To be able to duplicate,
ths recipients ID is kept as reference to update it for duplicated invoices.

`quantity` is a integer, the real quantity is the amount / 100, so 13600 = 136

`pocket-cfo-ctl new --recipient 7` stubs the file: recipient snapshot, next number, due date
from `payment_terms_days`, resolved tax regime and note. You type only the lines, then
flip `status` to `issued` when you're happy.
`pocket-cfo-ctl duplicate --invoice 1` duplicates the given invoice but updates recipient and issuer, invoice next number and due date of course
from `payment_terms_days`, resolved tax regime and note. You type only the lines, then
flip `status` to `issued` when you're happy.

status `draft` leads to rendering into .......-DRAFT.pdf, the only one that will always gets overwritten during CI CD

The same loop exists through the agent API (§11): a draft is created, edited and
re-uploaded there any number of times, and `issue_invoice` is the only way the flag
comes off — an issued document is exactly as write-once over the API as it is by hand.

### 3.4 Computation

All of it derived, all of it in `internal/money`, all in integer minor units with
`shopspring/decimal` for the intermediate arithmetic.

```
line.amount = quantity × unit_price          quantity absent ⇒ 1, so amount = unit_price
subtotal    = Σ line.amount
net         = subtotal − Σ discounts          see §3.5
vat         = per rate group: round(base × rate/100)
grand_total = net + vat
```

**VAT is computed on the summed base per rate, never per line.** Art. 226 of the VAT
Directive and чл. 114, ал. 1 ЗДДС both require the invoice to show the taxable base and
the tax amount *per rate*; neither knows about line-level tax. So: group lines by
`vat_rate`, sum each group's discounted base, apply the rate once, round once. Rounding
per line and summing gives a different figure, and it's the discrepancy an accountant
notices.

With one rate — which is every invoice you have — this is just `round(net × rate/100)`.
It only becomes interesting the day you invoice a Bulgarian client at 20%.

**Round half away from zero**, the usual commercial convention, and only at the point
shown above. Never round an intermediate.

Since totals are no longer in the JSON, the safety net is a test rather than a validator:
compute both reference invoices and assert 4.600,00 € and 10.200,00 €. That test is worth
writing before the template.

### 3.5 Discounts

Discounts **you** grant, on the invoice sum, before VAT. Not terms the client elects, so
no deadlines and no conditional arithmetic. A free-form array — put whatever you want in
it.

```json
"discounts": [
  { "label": { "de": "Skonto", "bg": "Отстъпка" }, "percent": 200 },
  { "label": { "de": "Treuerabatt", "bg": "Отстъпка за лоялност" }, "amount": 100000 }
]
```

Each entry has exactly one of `percent` or `amount` (minor units). Absent or empty means
no discount.

As everywhere also here we use integers as "money" calculation, percent 200 means 2% and amount 100000 means "1000"

**Applied in order, each against the running total**, not all against the original
subtotal. Two stacked 10% discounts take 19%, not 20%. That's the commercial convention,
but it's genuinely ambiguous, so it's stated here and pinned by a test.

They appear in the totals block in order, not as a footnote:

```
Zwischensumme          10 200,00
Skonto 2 %              −204,00
Treuerabatt             −100,00
Nettobetrag             9 896,00
Gesamt                € 9 896,00
```

Because discounts are applied before issuing, they reduce the taxable base directly and
there's nothing to correct afterwards. Granting one *after* sending an invoice is a
different instrument — a credit note (кредитно известие), not an edit to this array.

On a multi-rate invoice, a discount on the sum has to be split across rate groups
proportionally before VAT is computed, or the per-rate bases won't add up. Four lines of
code, impossible to notice when it's wrong, and irrelevant until your first 20% invoice —
write it now anyway.

### 3.6 Payment

Payment lives in its own file, `data/paid-invoices.json`, not in the invoice document:

```json
{
  "paid": [
    { "invoice": "INV-0000000002", "date": "2026-01-28" }
  ]
}
```

A date per invoice. Nothing else — no amount, no method, no partial payments. If you're
looking at the invoice at all, you know whether the money arrived. An invoice absent from
the list is unpaid, and a missing file means nothing has been paid yet.

It sits outside the invoice because an issued invoice is write-once (§1) while payment is
mutable: recording it inside the document meant every payment rewrote an immutable file,
and made the dashboard's staleness badge (§5.1) report a not-yet-rendered `-paid.pdf` as
if the JSON had drifted.

An entry is an object rather than a bare number-to-date pair so a bank reference can be
attached later without a schema break. `pocket-cfo-ctl validate` enforces what JSON Schema
can't: no duplicate invoice numbers, every number resolves to an invoice that exists, and
no draft is marked paid.

Recording a payment is also the one invoicing write the Hermes API offers
(`set_invoice_paid`, with `list_invoices` beside it): `paid: true` with the day the money
arrived replaces a wrong date as a correction, `paid: false` removes the entry, an
identical re-send is a no-op that commits nothing, and the optional free-text `note` is
where a bank reference — or which account the money landed on — goes. There is
deliberately no account or method field to record it in; if the date-plus-note ever
stops being enough, the schema grows a field and nothing else changes.

Partial payments are deliberately unsupported. If that ever changes, this field becomes
an array and the derived rules in §3.7 grow one line; nothing else in the design cares.

### 3.7 Annulment — deliberately deferred

Under чл. 116 ЗДДС an incorrect invoice is not edited or deleted — it is annulled, the
annulled document is retained, and a protocol records why. **None of that is built.**

It was once a schema field and a filter in `stats`, but with no renderer, no route, no
validator and no actual annulled invoice, it was a design being carried rather than used.
Carrying it cost more than re-adding it will: every refactor had to keep an untested
branch alive. So the field is gone from `schemas/invoice.json` and `stats` no longer
filters on it. Nothing in the design blocks on this.

**What comes back when the first annulment happens**, roughly as it was specified:

- an `annulment` object on the invoice — `date`, `reason_de`, `reason_bg`, and an
  optional `replacement_invoice`
- `INV-….-ANUL.pdf`: the original overprinted with `STORNIERT · АНУЛИРАНА · CANCELLED`,
  the reason, date and replacement number — the original PDF itself untouched
- annulled wins over every other derived state (§3.8), and annulled invoices leave the
  revenue and outstanding figures
- validation: reason in both languages, `replacement_invoice` resolves and isn't itself
  annulled

**The number stays consumed either way.** `INV-0000000003` remains in the sequence
forever — which is why annulment exists rather than deletion, since deleting would break
both the gapless-sequence check and an audit. `pocket-cfo-ctl delete` already refuses
anything that isn't a draft, so nothing today can violate that.

Annulment protocol vs credit note (кредитно известие: a `type: "credit_note"` invoice
with negative amounts and `corrects`) is a question for your accountant when the first
one comes up.

### 3.8 Derived state

```
paid       listed in paid-invoices.json
overdue    not listed, due_date < today          ← request-time only
open       not listed, due_date >= today

outstanding = Σ grand_total of issued invoices that are not paid
days_to_pay = payment date − issue_date
```

They are automatically computed during rendering of the webapp and never precomputed. They can technically be cached
but as the invoices are in memory anyway, it's probably a waste of time.

## 4. Tax regimes

### 4.1 Resolver

`tax.Resolve(issuer, recipient) → Regime`. Pure function, no I/O, table-tested against
both references.

| Recipient | Regime key | VAT | VIES | Evidence |
|---|---|---|---|---|
| BG | `domestic_standard` | 20% | no | — |
| EU + business + valid VAT ID | `eu_b2b_reverse_charge` | 0% | **yes** | `vies` |
| non-EU business | `outside_eu_place_of_supply` | 0% | no | `manual_document` |
| EU consumer | — | — | — | **error, block** |

The EU-consumer branch must fail loudly rather than silently zero-rating.

### 4.2 Note catalog — `catalog/notes.json`

**Accountant-owned.** These citations came from the accountant; do not "correct" them
from statute reading.

```json
{
  "outside_eu_place_of_supply": {
    "citations": ["ЗДДС чл. 69 ал. 2"],
    "text": {
      "de": "Steuerfreie Leistung gemäß Art. 69 Abs. 2 des bulgarischen Umsatzsteuergesetzes (Leistungsort außerhalb der EU).",
      "bg": "Данъкът не е начислен на основание чл. 69, ал. 2 от ЗДДС – място на изпълнение извън територията на страната."
    },
    "mandatory_wording": [],
    "vies_reportable": false,
    "reviewed_at": "2026-04-13"
  },
  "eu_b2b_reverse_charge": {
    "citations": ["ЗДДС чл. 21 ал. 2", "ЗДДС чл. 86 ал. 3"],
    "text": {
      "de": "Steuerschuldnerschaft des Leistungsempfängers (Reverse Charge). Keine Umsatzsteuer gemäß Art. 21 Abs. 2 und Art. 86 Abs. 3 ЗДДС.",
      "bg": "Данъкът не е начислен на основание чл. 21, ал. 2 и чл. 86, ал. 3 ЗДДС. Данъкът е изискуем от получателя (обратно начисляване)."
    },
    "mandatory_wording": ["обратно начисляване"],
    "vies_reportable": true,
    "reviewed_at": "2026-06-30"
  }
}
```

`reviewed_at` exists so that when the law changes you can see which entries predate it.

### 4.3 Validators — CI on every push, and as a pre-commit hook

Hand-editing has no UI stopping you typing nonsense, so this is the entire safety net.
It should be paranoid.

- **JSON Schema** on every file under `data/` and `catalog/`.
- `mandatory_wording` appears in the rendered note, or refuse. For EU B2B, "обратно
  начисляване" is a hard content requirement (чл. 114, ал. 1, т. 12), independent of
  citation style.
- **Gapless sequence**, no duplicate numbers, filename matches `number`.
- `due_date >= issue_date`; both parse as real dates.
- **`paid-invoices.json`**: no duplicate invoice numbers, every number resolves to an
  invoice that exists, and no draft is marked paid.
- **`discounts`**: each entry has exactly one of `percent` or `amount`; the running total
  never goes negative.
- **Translation complete**: every `de` string has a `bg` sibling.

The regime is cross-checked, not trusted: `tax.Resolve` is run over the parties the
invoice itself snapshotted, and a stored `tax.regime` that disagrees is refused, naming
both sides. A zero-rated regime whose lines carry VAT is refused too. The reverse is not
checked — a domestic invoice may legitimately zero-rate a line, and the resolver never
sees lines.

Two things the JSON Schema is checked with, rather than against: `pocket-cfo-ctl
validate` runs the raw bytes through the embedded schema (`schemas.Validate`) *before*
unmarshalling, because go-jsonschema generates no code for `additionalProperties`,
`oneOf`/`not`, `propertyNames`, `uniqueItems` or a non-string `const`. Unmarshalling
alone would let a typo'd key, a discount carrying both `percent` and `amount`, and a
misspelled regime key in the catalog through.

Note what is *not* checked:

- **Whether an issued invoice's content changed.** `git log -p data/invoices/INV-….json`
  is the record, and the PDF beside it was written once. A check here would be redundant.
- **The recipient snapshot against the current `data/recipients/*.json`.** That drift is
  the point (§2): rewording a recipient must not alter a document already sent.
- **That every regime in the enum has a catalog entry.** Only the regimes invoices
  actually use are required to have one — `catalog/notes.json` carries no
  `domestic_standard` entry, and its wording has to come from the accountant before the
  first BG-domestic invoice can be issued.

A consequence worth stating: because the wording check reads the *current* catalog, an
accountant adding a mandatory phrase retroactively fails older invoices. That is
intended for a legal content requirement — it should be loud — and `reviewed_at` is the
signal for that conversation.

---

## 5. Building

`build.yml`, on push to `main`:

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0        # required — git log is useless on a shallow clone
```

```
1  pocket-cfo-ctl validate     §4.3
2  pocket-cfo-ctl render       see staleness rules below
3  pocket-cfo-ctl index        build/index.json + build/stats.json
4  commit build/ back using the default GITHUB_TOKEN
```

Step 4 does **not** re-trigger the workflow, because commits made with the default
`GITHUB_TOKEN` don't fire workflow events. Use a PAT there and you get an infinite loop.

### 5.1 Staleness

Three rules, one per artifact.

| Artifact | Rule |
|---|---|
| `INV-….pdf` | Render if missing and status != `draft`. **Never overwrite.** |
| `INV-….-DRAFT.pdf` | Render if status is `draft`. Overwrite on each run of the action. |
| `INV-….-paid.pdf` | Render if the invoice is listed in `paid-invoices.json` and it's missing, or if the JSON is newer. |

"Newer" means per-file git commit timestamps:

```
git log -1 --format=%ct -- data/invoices/INV-0000000002.json
git log -1 --format=%ct -- data/paid-invoices.json
git log -1 --format=%ct -- build/INV-0000000002-paid.pdf
```

Both sources count, not just the invoice: the stamped date itself lives in
`paid-invoices.json`, so correcting a payment date has to make the PDF stale even though
the invoice never changed.

JSON timestamp greater → re-render. This works identically on your laptop and in CI,
needs no event context, and carries no state between runs. `github.event.before` would
also work in CI, but it's all-zeros on a first push, breaks on force-push, and silently
skips changes from a run that failed — and it doesn't exist locally at all, which is the
disqualifying part.

Locally, a file with uncommitted changes counts as newer than any timestamp. Check
`git status --porcelain` and fall back to mtime for dirty files.

### 5.2 Forcing a re-render

**Delete the PDF and push.** The next build regenerates it. No workflow inputs, no
dispatch job, no force flag in CI — absence of the file *is* the instruction, and it's
visible in the diff, which is better documentation than a workflow run buried in the
Actions tab.

Locally:

```
pocket-cfo-ctl render                          # everything stale or missing
pocket-cfo-ctl render INV-0000000002           # one invoice
pocket-cfo-ctl render INV-0000000002 --force   # overwrite even the original
pocket-cfo-ctl render --dry-run                # what would render, and why
```

`--force` exists only in the CLI. If you ever want it in CI, deleting the file is
already the simpler path.

Failure isolation: one bad render must not block indexing the rest. Collect errors,
publish what succeeded, fail the job at the end with a summary.

Secrets: `API2PDF_KEY`, `DEEPL_API_KEY`. Local runs use the same values from a `.env`;
there is no CI-only step.

Translation stays a separate manual-dispatch workflow that opens a PR. Machine
translation into a language you can't proofread shouldn't land unreviewed on a legal
document, and at this volume running it by hand costs nothing.

---

## 6. Rendering

```go
type Renderer interface { Render(ctx context.Context, html []byte) ([]byte, error) }
```

**HTML** — Go `html/template`, one canonical layout matching the reference PDFs.

**PDF — api2pdf.** `POST https://v2.api2pdf.com/chrome/pdf/html`, key in the
`Authorization` header. Use the **Chrome** endpoint, not wkhtmltopdf — Cyrillic shaping
is where wkhtmltopdf falls over. Retry with backoff on 5xx.

`outputBinary: true` is sent, and **api2pdf ignores it on this endpoint** — it answers with
its usual JSON envelope carrying a `FileUrl`, so the document does sit in their 24-hour
file store behind a link that is its own bearer token (the URL is fetched with no
Authorization header, because sending the key to a host api2pdf named would be worse).
Both response shapes are therefore handled: bytes inline if they ever arrive, the file
store otherwise. v0.26.0 required the inline form and every render failed on first contact
with the real service — if you are tempted to tighten this again, mock the *envelope*,
which is what the service actually returns.

**Fonts — Google Fonts, with two required settings.**

```html
<link href="https://fonts.googleapis.com/css2?family=PT+Sans:wght@400;700&display=block&subset=cyrillic,latin-ext" rel="stylesheet">
```

The family is PT Sans, which is what `templates/invoice.html.tmpl` loads and what every
issued PDF is set in. The two settings below are the part that matters — either font
covers Cyrillic, and changing the family now would restyle a legal document for no
reason.

- `display=block`, not `swap`. With `swap` Chrome paints fallback text immediately and
  substitutes later; capture in between and you get the fallback font, or tofu where it
  has no Cyrillic.
- Set `delay` (~1500ms) on the api2pdf request. Their Chrome is a fresh serverless
  container per call, so the font is fetched cold every time — the cache you're thinking
  of is your browser's, not theirs.

If you ever see boxes in the Bulgarian column, this is the cause, and base64-inlining a
subset is the fix.

**Both artifacts** are the same template with a flag in the context:

- **`INV-….pdf`** — plain. Written once.
- **`INV-….-paid.pdf`** — the invoice with a rotated `BEZAHLT · ПЛАТЕНО · PAID` stamp
  carrying the payment date. Rendered as soon as the invoice is listed in
  `paid-invoices.json`.
**The original is never touched.** чл. 116 ЗДДС and plain sense both require you to be
able to produce the document you actually sent, not a marked-up version.

The paid copy is a courtesy for clients who ask for confirmation — the payment record is
what settles the invoice in your books, not a stamp on a PDF.

In the app: original first, then the paid copy.

**Number formatting** — the two references disagree (`1.000,00 €` vs `10 200,00`). Pick
one per locale via `golang.org/x/text/message` and enforce it in the template.

---

## 7. Statistics

`build/stats.json`, all **date-independent** facts:

- Issued per month: count, total, split by regime.
- Collected per month, keyed on the payment date. Deliberately a separate series from issued — cash
  in versus work billed, and for a one-person company the gap between them is the number
  that actually matters.
- Per client: lifetime revenue, invoice count, average value, **average days to pay**
  (`paid − issue_date`).
- Regime split — feeds the VAT return and VIES recap directly.
- Outstanding per invoice, with `due_date` carried through.

Computed at request time: overdue set, ageing buckets (0–30 / 31–60 / 61–90 / 90+),
days-to-due countdowns.

Dashboard, in priority order — the top three are the ones you'll actually look at:

1. **Outstanding total**, overdue portion called out.
2. **Ageing table**, oldest first, one row per unpaid invoice.
3. **Cash collected**, last 12 months.
4. Issued vs collected, overlaid.
5. Revenue by client, current year.
6. Average days to pay per client — tells you which client to chase before taking the
   next job from them.
7. Regime split for the current VAT period.

At 30 invoices a year, render charts as inline SVG server-side. A chart library and its
build step would be more machinery than the data justifies.

---

## 8. Web app

Reads `data/` and `build/` from local disk (`DATA_DIR`/`BUILD_DIR`, see §2), same as
`pocket-cfo-ctl`. No database. It is read-only for invoicing; the one write path is the
agent API in §11, which commits to the data repo rather than to disk.

Reading `build/` from GitHub via the Contents API instead of disk, cached with an ETag,
remains a possible direction and is not built. It would buy little now that the image
carries its own data.

Auth: GitHub OAuth App, scopes `read:user repo`. On callback,
`GET /repos/{owner}/{repo}/collaborators/{login}/permission`; require `push` or `admin`.
Cache in an encrypted session cookie, 10-minute TTL. Everything behind auth — there is no
public route.

**Second, lesser-trust path: email login.** Anyone listed in the private data
repo's `$DATA_DIR/users.json` (see `internal/users`, `schemas/users.json`)
can request a login link at `/auth/email` — a signed,
self-expiring token (`internal/auth/otp.go`) emailed via Amazon SES
(`internal/mail`, AWS SDK for Go v2), valid for 15 minutes. Clicking it sets the same encrypted
session cookie, but with `Permission = "readonly"` and a 7-day TTL
(`auth.ReadOnlyTTL`) instead of 10 minutes — re-doing an email round trip
every 10 minutes would be unusable, and there's no GitHub-side permission to
re-check anyway. `readonly` sessions see the same dashboard as `push`/`admin`
ones (`authenticated()` gates `/` and `/invoices/{file}`), *except* the
client-portal links (`portalLinks()`), which stay exclusive to `authorized()`
— those are bearer secrets for the separate, even-lower-trust `/client/{token}`
tier and shouldn't leak to this one.

The login link is a self-expiring bearer token, not a server-tracked
single-use code — consistent with the stateless `/client/{token}` portal
links below and with this app's "no disk, no DB" design. It can be reused
until it expires, but it only ever reaches the address it was emailed to. A
generic "check your email" response is shown whether or not the submitted
address is allowlisted, so the form can't be used to enumerate valid
addresses.

| Method | Path |
|---|---|
| GET | `/` — dashboard |
| GET | `/invoices` — list, filter by state / client / period |
| GET | `/invoices/{number}` |
| GET | `/invoices/{number}.pdf` |
| GET | `/invoices/{number}-paid.pdf` — 404 until the invoice is listed in `paid-invoices.json` |
| GET | `/recipients`, `/recipients/{n}` |
| GET | `/reports/vies?period=2026-07` |
| GET | `/auth/login`, `/auth/callback` |
| GET | `/auth/email` — email-entry form |
| POST | `/auth/email` — request a login link |
| GET | `/auth/email/callback?token=...` — verify and log in (readonly) |

**IAM permissions for SES.** The credentials in `AWS_ACCESS_KEY_ID`/
`AWS_SECRET_ACCESS_KEY` need exactly one action, scoped to the verified
sending identity — nothing else the app does touches AWS:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AllowSESSendFromPocketCFO",
      "Effect": "Allow",
      "Action": "ses:SendEmail",
      "Resource": "arn:aws:ses:<region>:<account-id>:identity/<SES_FROM_EMAIL-or-its-domain>"
    }
  ]
}
```

`<region>` must match `AWS_REGION`; `<account-id>` is the AWS account the SES
identity is verified in. Not needed: `ses:SendRawEmail` (the app only sends
a plain-text `SendEmail`, never raw MIME) or any `ses:Get*`/`ses:List*`
identity-management actions (those are for verifying the identity yourself
via the console/CLI, not something the running app ever calls). If the SES
account is still in the sandbox, every address in `users.json` — not just
`SES_FROM_EMAIL` — must also be a verified identity, or production access
must be requested first.

Deploy: single Go binary, any host, scale-to-zero fine — no local state.

**Payment through the API, added since**: `set_invoice_paid` (and `list_invoices`) under
the Hermes surface — `POST /api/invoices/paid`, appending to `data/paid-invoices.json`
through the GitHub Contents API under `GITHUB_DATA_TOKEN`, rather than the visitor's OAuth
token sketched here first. Splitting payment out of the invoice document is what makes
this a small, additive write rather than a rewrite of an issued invoice. No database
appears, the commit is attributed through the token's identity, and GitHub enforces write
permission for you.

---

## 9. Testing

- **Golden tests on the HTML, not the PDF.** Both references as fixtures. Byte-comparing
  PDFs breaks the day api2pdf upgrades Chrome, reddening CI for reasons unrelated to
  your code.
- Table tests on `tax.Resolve` — CH, AT, DE, BG, GB, US, EU-consumer.
- **Computation tests first**: both references must total 4.600,00 € and 10.200,00 €.
  Then: two stacked 10% discounts take 19% of the subtotal; a mixed-rate invoice with a
  sum discount splits it proportionally across rate groups; VAT rounds once per group,
  not per line.
- Validator tests: AT invoice missing "обратно начисляване" rejected; sequence gap
  rejected; a `paid-invoices.json` entry that duplicates a number, names no existing
  invoice, or marks a draft paid rejected; discounts driving the total below zero
  rejected.
- **Staleness tests** over a git fixture repo, fake renderer counting calls: second build
  renders nothing; `status: draft` renders nothing; editing the template renders nothing;
  listing an invoice in `paid-invoices.json` renders only `-paid.pdf`; deleting `INV-….pdf` renders exactly that
  one; a shallow clone fails loudly rather than silently re-rendering everything.
- `stats` tests: paid before and after the due date.
- Nothing in the test job hits api2pdf.

The shallow-clone case deserves a real test, and it has one — though the symptom is not
the one predicted here. A `--depth 1` clone does not return empty timestamps: it
attributes every file to the single grafted commit, so every timestamp is *identical*.
Nothing then ever looks newer than anything, and a corrected payment date is silently
never re-stamped — the opposite failure, and a quieter one. `render` therefore checks
`git rev-parse --is-shallow-repository` and refuses to guess, rather than reasoning from
timestamps it knows are meaningless.

---

## 10. Finance tracker

Lives in `internal/finance`. Two ledgers, because a one-person company spends from two
different pots: **company** expenses come out of company income before anything becomes
salary, and **private** expenses come out of net income after payroll. A budget category
declares which by the `kind` of the group it sits in, and so does a bank account in
`accounts.json` — the same two words, because it is the same two pots.

**Income** is predicted from an hourly rate and, when Toggl is configured, from hours
actually tracked. Once an invoice is issued for a linked client, the invoice supersedes
the hours it covers — otherwise the same work would be counted as both a prediction and a
receipt.

**The two-month funding shift** is the domain rule that most of the module bends around:
money earned in month M is invoiced at the end of M, paid during M+1, and is spendable in
M+2. So the expenses shown for a month are funded by the income of two months earlier,
and every range that touches income is shifted against the range that touches expenses.
`funding.go` holds both, and they are derived from one function so they cannot drift.

**The salary cascade** turns company income into net personal income: company income less
company expenses, less the employer's contributions, gives gross salary; the employee's
contributions and income tax come off that. Every government-set figure in it — both
parties' contribution schedules, the tax bands, the minimum wage — is dated configuration,
never a constant in the code, because last year's payslip has to stay reproducible. There
are no built-in defaults; a month before the earliest entry has nothing in force and is
charged nothing, and the page says so rather than borrowing a plausible rate from a later
entry.

Schedules are **marginal bands**: a rate applies only to the slice of the base inside its
own band. That is what lets a contribution ceiling be an ordinary band with a rate of zero
rather than a concept of its own — "contributions stop at the ceiling" is one case of a
rate changing at a threshold, not the general rule. UK employee NI drops from 8% to 2%
above the Upper Earnings Limit instead of stopping, and UK employer NI never stops at all;
a ceiling field could express neither.

A month also declares **what salary is drawn** — full, the statutory minimum, a fixed gross
figure, or none — because choosing the minimum to leave money in the company, naming an
amount, and drawing nothing are ordinary decisions that the affordability arithmetic cannot
represent. `fixed` is the one mode that ignores affordability: it is paid whether or not
the money is there, and overdraws the company rather than shrinking. It is not, however,
allowed below the minimum wage in force — outranking what the company can afford is a
choice, and outranking the law is not.

**The company is a balance, not just a flow.** It used to be modelled as a pot that empties
every month, and `accounts.json` said so on purpose. That was only true because a full
salary hands the whole remainder to gross, which drains the company by construction. Once a
month can pay the minimum or a fixed figure, the rest stays behind, so a company balance is
carried like a private one: it enters the ledger straight after Company income and *before*
the cascade, raising what a full salary can afford (and lowering it when overdrawn), and
what the cascade leaves behind opens the next month:

```
company closing = opening + company income − company expenses − employer social − gross
                  − what else actually left the bank
```

That last term is the owner's own withdrawals and the taxes a distribution triggers, and it
is the one the plan cannot state: a draw is in nobody's budget. So the planned column charges
what the plan says will leave — the declared taxes — and the Actual column charges what the
statements say did. A month closes on one or the other, whole, never a mixture, for the same
reason a half-imported month falls back to the plan for its expenses.

Two things follow from that formula rather than from code written to produce them. A full
salary still closes the company at zero. And the closing figure is derived from the
**rounded cent fields**, not the floats behind them, for two reasons that both matter: it
seeds the next month, so a half-cent compounds down the year; and the rows shown on the
page have to add up to it, because that is the first thing a reader checks.

The balance being an *input* to the cascade is why the roll-forward runs before it rather
than after, and why both pots are walked in one pass — the company balance feeds payroll,
and the private balance is fed by what payroll pays out.

**An account keeps every reading, not just the last.** `accounts.json` used to hold one
`balance` and one `as_of` per account, so re-reading the bank wrote over the previous
figure — lossy in the one direction that matters, since the reason to read again is that
the carried figure has drifted. Readings live in a `balances` array now, and the one in
force for a month is the newest dated before it. A month before every reading still has no
balance; a month after a later reading opens on **that** reading rather than on a chain
extrapolated past it. So a yearly sync corrects everything from that point on without
rewriting the months before it, and paging back shows what was true then rather than a
number read a year later.

Both pots are still anchored at one date, because they are walked together, so an account
nobody re-read is carried as if its older figure were the anchor's. That drag is unchanged
from the single-reading file, and it is visible in the read dates on the spending page.

Two rules the schema cannot state are refused rather than guessed: two accounts sharing a
name — now two histories for one account, with nothing to merge them by, where the old file
silently summed them — and two readings in one month, which would be two candidate openings
for the next. A rejected file also says so on the page; switching the balance layer off in
silence is indistinguishable from having no `accounts.json` at all.

**Where a balance is explained, and where it is not.** The dashboard shows an account as a
name and a figure, nothing else. It is a page of arithmetic, and every clause that
accumulated around those rows — the read date, the free-text note, why the company is
overdrawn, how long since the bank was checked — was prose competing with the numbers it
annotated. The read date is real context, so it lives on the **spending page**, next to
Coverage: that is the page you are on while reconciling against a statement, and so the
page where learning the bank was last read six weeks ago actually changes what you do.
The `note` on a reading is documentation for whoever edits `accounts.json` and renders
nowhere.

**`targetBalance`** is a figure the company saves towards: while it is under, the month pays
the statutory minimum; at or above, full salary resumes. It is a **floor, not a high-water
mark**, and that distinction is the whole design. The target is subtracted from what payroll
may spend, so a full salary drains the company down to the target and stops. Switching only
the mode would oscillate — reach the target, pay it all out, fall under, back to the
minimum, forever — which is a reserve that is never actually reserved.

A target can only ever hold a month back, never make it pay more, so an explicit `minimum`,
`none` or `fixed` month wins over it. Writing a target over one is allowed rather than
refused, because the salary block is the more explicit statement; but it does nothing there,
so `/info` names every idle month and the page says so in the month itself. The same goes
for a target with no company account to measure against.

An **unknown** company balance is not a balance of zero. Treating the two alike would hold
every month at the minimum on no evidence, so the distinction is carried explicitly and the
target simply does not apply where nothing is known. The year view is the case that makes
this load-bearing: it reads no balances at all, and it stays that way on purpose —
`fundingIncome` spreads company expenses evenly across its range, which smooths a flow
harmlessly but would reorder a balance, so a twelve-month range would close the company
somewhere the twelve month views never do.

**Planned versus actual.** `budget.json` is what was planned; `actuals/YYYY-MM.json` is
what the bank statement said. Actuals move the **roll-forward and nothing else**. A month
is closed on its statements rather than its plan, so a balance carried from an old read
spends what was actually spent; but no planned figure of the month on screen changes, and
a test still asserts that, because it is easy to break by accident and invisible when
broken.

This used to be stricter — actuals were display-only and fed no figure at all. That made
every carried balance a statement of intent: the further from the read, the more the
number described the budget rather than the account. The drift was systematic and
compounded, and the evidence to correct it was already imported and being ignored.

A month is closed on its statements only when `coverage` spans the whole month. A
half-imported month closed on its transactions reads as a frugal one, and the surplus it
invents is carried forward for good with nothing on the page saying so; falling back to the
plan is the conservative answer, and `coverageComplete` already computed the test. Income
is untouched either way and stays predicted from Toggl and invoices, because statement
credits are `ignored` lines and actuals have nothing to say about it. Untracked money
counts as unspent, so an actuals close is optimistic by exactly the figure the untracked
marker already reports.

A figure can therefore be read off the bank, carried on statements, or carried on the plan,
and the dashboard does not distinguish them. It briefly did, as a tooltip on the opening
figure. The distinction is real but it is diagnostic: wanted when a figure is being argued
with, noise on every other visit, and not worth the machinery of threading a provenance
record through the roll to render it. What the reader has instead is the read date on the
spending page, which answers the question that actually prompts the doubt.

**A budget category is recurring, a one-off, bounded, or stepped.** `budget.json` has
four shapes for a category's amount. Plain `amount` recurs every month, unbounded in both
directions — a rent, a fixed subscription. An `amount` plus a `date` is a one-off, counted
only in that single month, shown as a grey estimate before it is due and dropped after;
a one-off is the only shape `move_planned_expense` can move. An `amount` plus `from` and/or
`until` is a recurring cost bounded to a window: it is counted in every month from `from`
onward (if present), up to and including `until` (if present), and nowhere else. The two
bounds are inclusive of their own month and the day on them is informational, matching the
`date` and `overrides[].month` convention, and `date` is mutually exclusive with both.
An `amount` plus an `amount_changes` list is a stepped recurring cost: the same category,
but its amount moves on the dated months the list spells out — a rent that rises every
January. The list is mutually exclusive with `date`, but not with `from`/`until`: a stepped
cost can also be bounded, and it obeys the window like an unstepped one.

That window is deliberately not spelled by overloading `date`. A subscription that ended in
August is "counted in every month through August, then gone", which is a different fact
from "a one-off that only ever existed in August": dating it rewrites every earlier month's
plan to zero, flips its genuine earlier charges to `unbudgeted`, and makes a retired
recurring cost look movable. So a bounded cost keeps its history through its `until` month,
and — like one that recurs monthly — is refused by `move_planned_expense`. Outside its
window the category is simply not shown: no announcement of the cost a `from` month will
bring, no 0,00 row for the one an `until` month retired, and an emptied group goes with
them. The one exception is a charge recorded against such a category — a mistake, since the
plan counts nothing there — which `ApplyActuals` restores the row for, under its own name
and at its original place in its group, planned at 0 and flagged `unbudgeted`, so the money
is visible where it can be fixed rather than swallowed by the nameless "not in this
month's plan" figure.

**A stepped amount resolves the latest entry at or before the month.** The top-level
`amount` is the first, unnamed period, and each `amount_changes` entry — `{from, amount,
minimal_amount?}` — opens a new one on its month: a month pays the latest entry whose
`from` is on or before it, the top-level amount where none is. That is the same reading
as `legislation` and `target_balance`, and it keeps every month's figure reproducible
from the file alone. `minimal_amount` is per entry and falls back to the entry's own
`amount` for the months that entry is in force — the top-level rule, repeated at the
level that owns the months. An `overrides` entry still wins over everything, including a
step, and month view shows the usual override marker. Two things a step is not, enforced
in `ValidateBudget`: a step on a one-off (a single price, full stop, has no months to
change in), and an entry outside the category's own window — before its `from` or after
its `until` it could never take effect, so it is dead config and is refused rather than
silently ignored. Duplicating a month is refused for the same reason: two prices in force
at once is a contradiction, not a schedule. The web view points at the change: a category
with a step inside the horizon shows a small arrow beside its name, and following it
opens the month the price moves.

**An imported month has two balances.** The plan charges the whole month on the first, so
the plan and the statements answer different questions — where the month was meant to end
up, and what the account actually holds. Both are shown, in the Planned-against-Actual
columns the ledger already uses. Neither replaces the other: the bank figure is optimistic
by whatever has not been imported or assigned yet, and only the planned one beside it makes
that legible. Mid-month that gap is most of the month; in a closed month it is the
overspend or the underspend, and the bank figure is then not an estimate at all — it is
what the month ended on, and what the next month opens with. A month nobody has imported
has only the plan to show, and a year view has no opening balance to build the second
figure from. The company row gets the same pair, with the expense figure substituted but
the cascade left alone — re-deriving affordability from a half-imported month would invent
a bigger salary purely because the statements had not caught up, and would break the rule
that the closing figure equals the rows above it.

Every statement line carries exactly one disposition: a budget category, an `ignored`
reason, an `untracked` note for money not yet decided, or `splits` when one line paid for
more than one thing. Absence is never a decision — the file accounts for every row on the
statement, so a skipped line is recorded as skipped rather than inferred from silence.

**A dividend is the other way the company hands its owner money**, and it is taxed on both
sides: company profit tax on the way out, dividend tax on the way in. Only the gross is
written down, in `budget.json`, dated; both rates are dated `legislation` in `config.json`
like every other government-set figure, so a distribution made under last year's rates stays
reproducible when this year's move. The day on it is informational and the two-month funding
shift does **not** apply — that rule is about when invoiced work becomes spendable, and a
dividend is a payment made on a date.

The ordering follows the side the money falls on, and it is a reading decision as much as an
arithmetic one. Company profit tax is a company cost, so it sits with the company's own costs
above the payroll cascade where it cannot read as reducing net income. The dividend behaves
like a gross and its tax like income tax, so the pair brackets the personal rows.

**Declaring a dividend moves no money.** It hands the owner a claim, and the director's loan
is what carries it; only the two taxes leave the bank. This is not a nicety — it is the
difference between a distribution being possible and not. Clearing a loan of 17,000 needs a
gross of 17,894.74, and a company with 204 in the bank can declare it, because the 17,000
never moves: it settles against what the owner already drew. Only 2,684.21 of tax is money
that has to be found. Charging the gross to the bank reported that company as 25,816
overdrawn when it was 7,921, and made the one instrument that fixes its position look
unaffordable.

So the company's cash and what the company is **worth** are two figures:

```
company worth = company cash − the director's loan
```

with the loan owner-centric, positive meaning the company owes him. The distribution costs
the worth its gross plus the profit tax; it costs the cash only the two taxes. Both are true
at once and the page shows both, the worth beside the accounts and the cash in the cascade.

The two taxes are charged **before the salary is sized**, not only at the close. A full salary
takes the whole remainder, so subtracting them afterwards would drain the company straight
through the target balance that is documented above as a floor. Charged first, both stated
invariants survive: a full salary still closes the company at its target, and the rendered
rows still add up to the closing figure. The rule underneath, which no type can express: **the
set of figures charged before the salary is sized must be the set charged at the close.** A
`fixed` or `minimum` month is unaffected except at the close, which is already how those modes
overdraw. A dividend in a month with **no rate in force is refused** rather than charged at
zero — unlike a salary, which happens in every month the navigation offers, a dividend exists
only where somebody deliberately wrote one, so the refusal is contained to that month and 0%
presented as a computed figure is not.

**Why a gross salary still leaves the company and a gross dividend does not.** Not because
one is more real than the other. A salary is settled every month by a transfer and its
remittances, so charging the gross is a one-month-lag approximation of a flow that does get
settled, with no liability accumulating behind it. A distribution is settled irregularly, or
never in cash at all — which is precisely why a director's loan exists for it and not for
salary. Making them symmetric would cost the "a full salary closes the company at its target"
property that the whole `targetBalance` design rests on, and buy nothing.

**The director's loan** is the running balance between the owner and the company: what the
company owes, or what the owner owes it. Positive means the company owes the owner. It
accrues by net income each month — which now includes a dividend net of its dividend tax,
and that is what makes "net income is what the company owes me" true rather than roughly
true — and is settled by money that actually crossed.

A marked line moves three figures, and which three is not the same question. Whether it
**settles the loan** is whether the money reached the owner; whether it **left the bank** is
whether the company is poorer for it; whether it **reached the owner outside payroll** is
whether the plan had not already assumed it would. A salary transfer does the first and
neither of the others — the cascade has already charged the gross, and counting the transfer
again would pay the salary twice on the company side and credit it twice on the private one.
The two taxes do the second alone: they leave the company for the state and never reach the
owner. A draw, a distribution paid out and money paid back in do all three. Transposing those
sets is the easiest mistake in this module, which is why all three answers are pinned value by
value over the schema's own enum.

The third set is the second question the private side had to answer, and the salary is the one
value that separates them. The paragraph above is the whole reason: a salary is settled every
month by a transfer, so the private figures are right to expect it and must not count the
transfer as well; a distribution is settled irregularly or never in cash, so the private side
may only count one that actually arrived. Excluding it there instead would make the private
figure optimistic by the net dividend in exactly the months a distribution is declared —
which is the case a loan exists for.

Two limits worth knowing rather than rediscovering. A month nobody has fully imported carries
the company's cash **high** by whatever the owner drew in it, and carries the private side
**low** by the same draw, because a half-read month closes on its plan whole rather than on
part of a statement. And a distribution declared in an unimported month charges its taxes to
the plan, while the month the tax is actually paid charges them again if that month *is*
imported; the carried balance is then light by that tax until the next bank reading re-anchors
it. Both are the ordinary cost of the plan-or-statement switch, and both are bounded by the
next reading.

A crossing is a statement line carrying a `movement` marker **beside** its `ignored` reason,
never instead of one: such a line genuinely is not a budget expense, so the exactly-one-of
rule above is untouched and a marked line still says why it is out of the figures. Two of the
six values leave the company for the state and cross nothing; they are marked only so the
spending page can list them rather than burying them among the ignored lines.

Both statements are imported, so the same transfer arrives twice, and counting it twice is
the one way this figure goes quietly wrong. **The sign is what enforces recording it once**:
everything except a contribution must be money leaving the company, so the mirror line on the
private statement — whose sign is the other way round — cannot be marked at all. That costs
nothing and, crucially, needs no resolving of a transaction's `account` against
`accounts.json`, which is deliberately not done. The residual case the sign cannot catch, two
company-side lines marked for one real transfer, is a note beside the figure rather than a
refusal: two genuine draws of one amount on one day are a real thing, and failing the month
would take it off the dashboard entirely.

Its opening figure lives in `accounts.json` as a series of dated readings — same shape as an
account balance, so a year-end restatement from the accountant is an ordinary append that
corrects everything after it without rewriting what was true before, and a month before every
reading has **no known figure rather than a figure of zero**. It sits beside the accounts
rather than becoming a third `kind` of one, and not only because a relationship between two
pots is not a pot: the balance snapshot treats anything that is not `company` as private, so a
reading would have been swept into the private opening balance, and it takes its opening month
as the latest across all accounts, so a loan restated in December would have dragged the
anchor both real balances are walked from. Living outside that array is what makes "it reaches
no other figure" structural rather than a rule somebody has to remember.

Because the two anchors are unrelated and either can be the earlier, the roll-forward starts
at whichever comes first and gates each figure on its own — otherwise the months in between
would silently never accrue. An unimported month settles nothing rather than falling back to
the plan, because the plan holds no transfers; the loan then reads higher than it is, and says
so. The year view has none, for the same reason it reads no balances at all.

The loan figure itself reaches the company's worth and nothing else — no cash figure, and no
input to the cascade, so a salary is never sized against money the owner has not given back.
The crossings it is built from are not private to it, though, and that is the correction §10
was missing longest: a draw is one transaction with two sides, and booking only the liability
left **Available to spend** reading low by every draw the owner had taken. It now books both.
The asset side lands in the **Actual column only** — a draw is in no plan, so the planned
column may not know it happened — which is the same planned/actual pair `Balance` and `Left in
the company` already show, and is why no budget figure moves when a statement is imported.

What each column assumes is the whole of it. The planned column assumes net income arrived,
because that is all a plan can say. The Actual column assumes only the **net salary** arrived
and counts everything else from the statements, for the reason two paragraphs up. So the two
columns agree exactly when the company transferred the net salary the plan sized and nothing
else crossed, and the residual between them is net salary accrued but never transferred —
which is the loan's own movement row. Neither figure has to guess, and a test pins the identity
because nothing in the type system ties the exclusion set to the internals of net income.

Settling the loan — by paying a salary large enough, or a dividend — is a decision taken with
the accountant, so neither the distribution nor the loan's opening figure is writable through
the agent API, only readable.

## 11. Agent-facing write API

`internal/api` is one service with two adapters: REST under `/api` and MCP at `/mcp`.
Both are gated by a bearer token, and when it is unset neither is registered at all — an
unauthenticated caller gets a 404 and learns nothing about the surface. The service knows
nothing of HTTP; the adapters translate and never compute, which is what stops the two
drifting apart.

Reads cover the budget, a month's transactions, a description search over committed
history, and a reconciliation status per month. The search is deliberately the substitute
for a rules file: an agent asks how a merchant was treated last time and gets the answer
from what was actually recorded, rather than from a list that drifts out of sync with it.

Writes are narrow by construction. Transactions can be **added** and **re-attributed**, a
planned one-off can be **moved** to the month it was really charged in, the recurring
amount of a category can be **scheduled to step** from a future month on, and an account
balance can be **recorded** for a month that has closed. Nothing can be removed — there is
no flag, no override and no reason that unlocks it, and each write path checks its own
output before committing rather than trusting itself to stay append-only. A line recorded
in error is repaired by a human editing the data repo, where the change is reviewed like
any other. Calling a *scheduled* step off is not a removal in this sense: it takes down a
future change before it has taken effect, and a month that is already in force it cannot
touch at all — an already closed budget is fixed in the file.

**Invoices have three writes, two of them about drafts.** `set_invoice_paid` records a
payment beside the document (§3.6). `save_draft_invoice` uploads a whole invoice document
**as a draft, always** — whatever `status` it carries is overwritten with `draft`, so
creating or flipping to issued is not expressible. A `number` left out creates: the next
number is assigned as max + 1 through a directory listing (`Store.List`), which keeps the
gapless-sequence check (§4.3) true without trusting the caller to count; a `number` that
names an existing draft replaces it, as often as the agent likes, because a draft is a
working document; a number that names an issued invoice is refused forever — the §3.3
write-once rule holds across the API, and a correction is a new invoice rather than an
edit. `issue_invoice` is the one way the flag ever moves: it splices `"draft"` →
`"issued"` in the raw bytes and nothing else, so the committed diff is one line however
the file is formatted — the same readable-diff discipline the budget byte-surgery uses —
and the result must still pass the schema and the §4.3 validators before it is committed.
Both write paths run the full invoice validators, including the regime cross-check and
the mandatory wording of the note catalog, and **refuse to commit when the catalog cannot
be loaded** rather than skipping a legal content requirement in silence. An upload
identical to what is committed changes nothing and commits nothing.

**A draft saved through the API reads back at once; its PDF does not.**
`get_invoice_document` reads the data repo through the store, so it is current the
moment the commit lands; the `-DRAFT.pdf` is rendered by the CI build that runs after
the deploy, so `GET /api/invoices/{number}/pdf` can answer 404 for a few minutes —
it says so, and the answer is to retry after the deploy rather than re-upload. The
variants are guarded by state, not by file presence: an issued invoice's `-DRAFT.pdf`
may still be on disk from before it was issued, and it must not be served.

**A balance closes a month, so `as_of` is a month end and anything else is refused.**
The reading is that month's closing figure and therefore the next month's opening one, and
only the month is ever read off it. A date in the middle of a month would be filed as that
month's figure while the rest of the month's spending had not happened yet, opening the
next month with money already gone — so it is refused outright rather than silently
rounded, as is a month that has not ended, since a balance is read off the bank and never
projected. The rule belongs to the data rather than to the API: `ValidateAccounts` enforces
it too, so a hand-edited `accounts.json` carrying a mid-month reading fails to load and
says why, instead of anchoring every month after it on half a month's spending. Readings are appended and a month that already has one is a conflict: the
history is what lets a past month keep the figure that was true then. Accounts themselves
are not created here, because an account declares which pot it belongs to and that decides
which side of the payroll cascade the money sits on.

**A step may only start in a future month, so an in-force `from_month` is refused.** A
month that is already in force is an already closed budget, and the answer to fixing it is
the data repo, not a tool that re-plans the past — so the refusal says exactly that and
points at `budget.json`. The same guard is why a one-off is out of reach at all: it has
one price and no months, and a step outside the category's own window could never take
effect. The write itself is the byte-surgery `move_planned_expense` uses: one category's
`amount_changes` is spliced in place, the diff guard refuses a result that differs from
the original by more than that one field, and the result must survive
`ValidateBudget` before it is committed — the file is hand-maintained and the commit
diff has to stay a few lines a reviewer can read.

Every accepted write is a **commit to the data repo through the GitHub Contents API**,
which redeploys the app. Nothing is written to the running container's own data directory:
that is an image layer or a mount, so a write there would be lost on restart and diverge
from the repo. This is what makes handing an agent an API tolerable — it is not trusted,
it is audited, and every change can be read in `git log` and reverted.

**Read-after-write.** A commit takes minutes to become a running image, so between the two
the app serves the bytes it just committed from memory, keyed by month. The overlay sits in
the tracker's loaders rather than in this package, because `Actuals.month()`,
`Budget.File()` and `Accounts.File()` are the single funnels every read passes through —
one insertion point makes the API and the web pages agree, where four call sites would
eventually disagree. An
entry is dropped as soon as the file on disk says the same thing, so nothing has to guess
at a deploy's duration.

It is memory, never disk. Writing the file would create a second source of truth, be
replaced by the very deploy the commit triggers, and diverge between instances. The overlay
cannot disagree with the commit because it *is* the commit, and it carries bytes through the
same parse and validation a file gets, so it can surface nothing a file could not.

The limit is per-process: a second instance has no overlay from the first, and a change made
outside the app appears only once deployed. Both are stated in the tool descriptions.

**A GitHub webhook is the obvious way to close that, and is deliberately not built.** It
would not actually close it: a webhook is delivered to one instance through the load
balancer, so the other instances stay exactly as stale — making it work would need fan-out
across them, which means a shared store or a pub/sub channel, and at that point the overlay
should live in the shared store instead. Against that: the commit triggers a deploy that
restarts every instance within the same few minutes, which is total invalidation for free;
`min_machines_running = 0` means one machine is the normal case; and a webhook is a new
public endpoint with a secret to hold, for a few minutes of freshness on a second machine
that usually is not running. If cross-instance freshness ever does matter, the honest fix is
a shared store for the overlay, not a notification.

`docs/HERMES.md` is the contract the agent works to, and the MCP tool descriptions are
where it actually reads it.

## 12. What is built, and what is deliberately not

Steps 1–8 of the original build order are done and released: schema and `money`,
`tax.Resolve` and the §4.3 validators, the template against both reference invoices,
`pocket-cfo-ctl render` through api2pdf, `build.yml`, the index and the web app, and
payment in `data/paid-invoices.json`. The finance tracker was added alongside them and
has its own conventions inline in `internal/finance`.

What remains is additive, and each item is deferred for a reason rather than pending:

- **Annulment** (§3.7), when the first one actually happens — schema, validators,
  `-ANUL.pdf`, stats exclusion. Confirm the annulment-versus-credit-note split with the
  accountant then; guessing at it now would bake in the wrong one.
- **`pocket-cfo-ctl new` scaffolding**, once it is clear what it should fill in.
- **DeepL as a PR-opening workflow**, and **VIES plus monthly reports**.

---

## 13. Settled questions

- **Number format.** Frozen as `10 200,00 €` — non-breaking space for thousands, comma
  for the decimal, symbol last. `render.formatMoney` is the only implementation and
  `TestFormatMoney` asserts the literal bytes, because "looks right" and "is right"
  diverge silently on an invisible U+00A0.