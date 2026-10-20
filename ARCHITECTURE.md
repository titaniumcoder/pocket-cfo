# PocketCFO — Architecture

Two halves in one binary: **invoicing**, and a **finance tracker**. Sections 1–7 and 10
are the invoicing side, which is the older and more constrained of the two; §8 covers the
web app that serves both; §10 the finance tracker; §11 the agent-facing read API.

**Code and data are separate repos.** This one is public and MIT licensed and carries
fabricated sample data; a private data repo holds one business's real data and bakes it
into the published image. See README.md for setting up the second one.

**One JSON file per invoice, and CI builds the PDFs.** Invoice data is edited by hand and
committed; it is never edited by the app or by the agent surface (§11) — an agent asked
for a change names the file and the edit and stops, and a person commits it.

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

The optional `tracking_client_id` ties the recipient to a Toggl client: invoices for it
scope the estimate machinery (§10) to that client's hours, and its tracked work shows on
the dashboard as invoiced work rather than as an unlinked mistake.

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
  "tracking_until": "2026-01-10",
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

**A line description carries a small formatting subset**, because a line is often
several things ("Migration of the mail server: — export the mailboxes — reimport them").
A newline in the string is a line break, `**bold**` and `*italic*` are emphasis, and
consecutive lines starting with `- ` render as a bullet list. The subset is deliberately
not markdown: the text is HTML-escaped **before** the formatting pass, so the pass works
on escaped text and can produce nothing but those three shapes — a `<` in a description
stays a literal `<` in the PDF. A description that uses a line break or a bullet renders
in the block layout; a one-line description keeps the compact inline one. The legal tax
note stays plain text by the same token: it is drafted in the note catalog, not typed
per invoice.

A line may also be **pure text** (`"type": "text"`): prose that spans the whole table
width instead of wrapping inside the description column — an explanation, a headline for
the positions below it. It carries no amounts at all: quantity, unit, unit_price and
vat_rate must not be set on one, which the schema enforces with if/then conditionals the
dev-time generator ignores but the runtime validator does not. It keeps its place in the
line order, contributes nothing to subtotal, VAT or totals, and splits across pages like
text rather than like a row that must stay whole.

`pocket-cfo-ctl new --recipient 7` stubs the file: recipient snapshot, next number, due date
from `payment_terms_days`, resolved tax regime and note. You type only the lines, then
flip `status` to `issued` when you're happy.
`pocket-cfo-ctl duplicate --invoice 1` duplicates the given invoice but updates recipient and issuer, invoice next number and due date of course
from `payment_terms_days`, resolved tax regime and note. You type only the lines, then
flip `status` to `issued` when you're happy.

status `draft` leads to rendering into .......-DRAFT.pdf, the only one that will always gets overwritten during CI CD

A draft is created and edited by hand in the data repo any number of times, and flipping
`status` to `issued` is the only way the flag comes off — an issued document is
write-once however it is changed.

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

**When an invoice counts as income is the owner's call, written down.** The default rule:
an invoice is usable the month after it fell due. `data/usable-invoices.json` overrides
it per invoice — an early-paying client whose January invoice should fund January, a
December invoice booked in January. Only written-down overrides live there; the file
staying empty is the ordinary case, and an override cannot move income before the
invoice was due (`pocket-cfo-ctl validate` refuses a month that precedes the due month).

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

Recording a payment is a hand edit to `paid-invoices.json` in the data repo — the agent
API's write tools are gone (§11), so `pocket-cfo-ctl validate` is what checks the edit.
The optional free-text `note` is where a bank reference — or which account the money
landed on — goes. There is deliberately no account or method field to record it in; if
the date-plus-note ever stops being enough, the schema grows a field and nothing else
changes.

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

The staleness badge (`IsCurrent`) judges each artifact against its manifest entry, and
the two kinds of entry pin different things because the two kinds of artifact fail
differently:

- **Issued PDFs** are write-once, so their entry records the PDF's own bytes — an
  immutability check. Re-deriving HTML from the *current* template and comparing it to
  a hash captured under an older template would flag every issued invoice stale after
  any template change, with no way to become current again short of violating
  write-once. Template changes therefore leave issued invoices current; the only drift
  the badge can report is the PDF file itself no longer matching its pin.
- **Draft PDFs** are re-rendered on every run, so their entry records the rendered
  HTML, which the badge re-derives from the current template.

Entries written before the two-hash manifest (plain hash strings) load as HTML pins;
the next `pocket-cfo-ctl render` run migrates them to PDF pins by hashing the stored
PDF — without touching it.

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
- Set `delay` on the api2pdf request (`printDelay`, 3000ms). Their Chrome is a fresh
  serverless container per call, so the font is fetched cold every time — the cache
  you're thinking of is your browser's, not theirs. The delay now has to cover the font
  *and* the paged.js pagination pass below, which needs about a second for a long
  invoice; capturing mid-pagination prints an empty document.

If you ever see boxes in the Bulgarian column, this is the cause, and base64-inlining a
subset is the fix.

**Pagination — paged.js.** Chrome prints `position: fixed` on every page but offers
nothing else from paged media: no running headers, no way to keep one element off page 1.
A fixed letterhead was the obvious cheap answer and fails twice — it cannot be kept off
page 1 (which already carries the full header), and it cannot reserve room for itself, so
it collides with flow content on every continuation page. Offsetting it upward does not
paint into the margin either: Chrome clamps an overflowing fixed element onto the *next*
page instead. paged.js polyfills what's missing, pinned by version and SRI hash:

- The running header (logo, issuer, invoice number) lives in a `@top-left` margin box via
  `content: element(running-header)`, suppressed on page 1 with `@page :first`. Page
  margins moved from body padding to `@page`, which is what gives every continuation page
  a real top margin. The margin box carries the strip only — no column-header row, which
  first lived there and ate the whole margin: with it, the content started flush against
  the header and the page read as having no margin at all. Without the column row the
  table keeps its natural auto layout, the columns no longer need to be fixed, and a page
  whose table is long finished (payment card only) is not headed by column labels for a
  table that isn't there — the trade is that a table continuing across pages repeats no
  column headers, which ordinary invoices tolerate.
- paged.js drops `position: fixed` outright, so the draft watermark and paid stamp are
  lifted out of the flow by the `PageStamps` handler and re-anchored into each laid-out
  page. Without paged.js they keep their native fixed behaviour; the handler never runs.

Three constraints are load-bearing, each learned by breaking it:

- **Column widths live on the cells, not on `<col>` elements** — paged.js drops cols, and
  with `table-layout: fixed` every table fragment falls back to equal widths. The running
  header's column row and the fragments read the same nth-child widths, which is what
  keeps them aligned.
- **Summary, notes and payment are table rows** (`tr.afteritems`), not boxes after the
  table. A final page holding only the payment card would otherwise show the margin-box
  column headers above nothing. An earlier attempt repeated the real `thead` with an
  `afterPageLayout` handler instead — inserting content after layout swallowed the
  following page's content, which is why the margin box carries the columns.
- **The running header's logo is a second inline copy with rewritten ids** (`rhlogo`):
  two svgs sharing gradient ids make `url(#…)` resolve against whichever copy sorts first
  in the document, and the page-1 logo went invisible exactly that way.

If the CDN is unreachable, printing degrades to native Chrome: margins still come from
`@page`, the column headers still repeat via `thead`, the stamps stay fixed, and a
fallback script removes the running header from the flow. The committed sample PDFs in
`build/` are pinned by `build/render-manifest.json`, whose entries say what each
artifact must keep matching — pinned to the rendered HTML for drafts (which get
re-rendered on every run) and to the PDF's own bytes for issued documents (§5.1); a
template change therefore leaves every issued sample untouched, and the committed
samples were rendered with the same headless Chrome locally.

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
`pocket-cfo-ctl`. No database. It is read-only — the agent-facing API in §11 is too;
every data change is a git commit in the data repo, never a write to the app's disk.

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

Payment is a hand edit to `data/paid-invoices.json` in the data repo; `list_invoices`
reports the states over the read API. Splitting payment out of the invoice document is
what keeps the edit small — one entry appended, never a rewrite of an issued invoice.

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
  invoice, or marks a draft paid rejected; a `usable-invoices.json` override for a draft,
  an unknown number or a month before the due month rejected; discounts driving the
  total below zero
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

**Income** is what the issued invoices brought in, plus an estimate for the worked
months no invoice covers yet — the two paragraphs below carry the whole rule.

**Toggl comes in two APIs**, and the tracker reads either or both (`TOGGL_MODE`; unset
means Track whenever its credentials exist, so a deployment that adds the 2.0 variables
changes nothing until it says so). Toggl
Track's Reports API does the accounting server-side: it returns billable entries already
rounded to the nearest quarter hour and priced per row. Toggl 2.0 (focus.toggl.com) has
no such report, so `toggl_focus.go` reproduces it from raw time entries — billable
activities only, undeleted, each rounded to the nearest quarter hour (half up, as Reports'
`rounding: 0, rounding_minutes: 15` does), priced at the project's billable rate in force on
the entry's day, falling back to the workspace rate and then to zero — and folds them into
the same per-project, per-rate, per-month aggregates, so nothing downstream knows which API
answered. `both` merges the two years, summing aggregates with the same project, rate and
month, for the migration in which hours are split between the products without live sync;
a project id present in both is shown under its 2.0 name with the hours added, tolerable
only because Track ids are nine digits and 2.0 numbers afresh. One side failing fails the
year: half a total would read as the whole.

The 2.0 API differs in three ways that shaped the client. Its keys (`toggl_sk_…`, sent as
a Bearer token) **expire** after a period chosen when they are generated, and only one is
active per user; from then on every call answers 401, and because the cache keeps serving
the last good hours after that, the returned errors would never show it — so the client
records the rejection itself, and `KeyStatus` turns it, or an optional
`TOGGL2_API_KEY_EXPIRES_AT`, into the one warning the finance page and `/info` render, above
the pending and stale notes. It offers **no call that lists the caller's organizations or
workspaces** (the one call that would, the workspace `/context`, answers a browser session
only — an API key gets 403 "this endpoint requires session authentication", confirmed live),
so both ids are configuration, read off the focus.toggl.com address. A key alone still
boots: it never reaches the dashboard, and `/info` says which two variables are missing. And
it is **quota-limited per hour** — as is Toggl Track since June 2025 — which shaped the cache
below. Confirmed against the live
API: an unknown key gets a 401 with a JSON error body, `per_page=200` is refused with a
validation 400 (the client asks for 50 and halves on that error), and the workspace
`/context` call answers 403 to an API key. Read from the published OpenAPI
spec and still to be confirmed with a real key: `duration` is in seconds, `date_from` and
`date_to` accept RFC 3339 timestamps, `hourly_rate` on the billable-rates endpoints is in
hundredths of the currency, and `per_page=200` is honoured.

**Toggl meters requests per hour, per user, per organization** — 30 on Free, 240 on Starter,
600 on Premium — and answers HTTP 402 beyond that for the rest of a sliding sixty-minute
window. Its documentation also promises `X-Toggl-Quota-Remaining` and
`X-Toggl-Quota-Resets-In` on every answer; confirmed live in September 2026, neither API
sends them (the 2.0 time-entries and billable-rates calls answer with `X-Service-Level:
GREEN`, a health indicator, and nothing about the quota), so the client reads them if they
ever appear but relies on the 402 alone. A
freelancer's year is some thirty pages of fifty entries, so re-pulling it every fifteen
minutes was the whole Free quota, four times over, and every cold start paid it again. The
cache (`toggl.go`) is therefore **one entry per calendar month**, and a fetch covers one
contiguous run of stale months in a single request: a cold year is still the one request it
always was, but the background refresh (`warm.go`) marks stale only the current month — and
the previous one through the 7th, while last month's entries are still being tidied — one
request for one or two months — and, every 42 hours, everything older, since an old month
changes only when someone corrects it. Reload of a month refetches that month alone,
and the status and pending notes follow the months in the viewed range, so the year view
reports the age of its oldest month. Month bounds are drawn in the process location, so a
day starts at local midnight on both APIs; an entry Toggl returns for a month it does not
belong to (a timezone edge) is kept under its own month rather than dropped. The rate
timelines stay cached until Reload, as before.

A **402 is a gate, not a failure**: it closes the whole backend until the reset a header
names, or for an hour when none does (plus ten seconds), the last good hours are served
meanwhile, the breaker is not touched, and the finance page and `/info` say when hours
refresh again instead of polling. The warmer skips a tick when the gate is closed or, should
Toggl ever report a remaining count, when fewer than five requests are left, so page views
keep them. The log notes the first answer of every endpoint with its header names, which is
how the absence was established. The cache is written to `TOGGL_CACHE_DIR` when that is set
(`snapshot.go`; one JSON file per backend, written atomically after every successful fetch
and on Reload, read back before the first lookup, ignored if unparsable; the directory
carries a `VERSION` marker, and a build whose format differs deletes every cache file
there — only `.json` files at the top level, so a misconfigured path can lose nothing
else — and writes its own marker before it reads anything) so a Fly machine woken from auto-stop, or a fresh deploy, serves the last hours
at once and asks Toggl only for what the cadence says is due — the one thing the app writes
to disk, and deliberately not the data repo, whose commits are the business record. Because
such a file outlives every restart, `/info` carries a **Reset Toggl cache** button
(`POST /toggl/reset`, admin-only): it drops every cached month, project and rate on both
backends, deletes the file, discards any fetch still in flight, and keeps only the quota gate,
which is real regardless of what was cached.

**Toggl webhooks are deliberately not used.** Toggl 2.0 has none — its OpenAPI spec lists no
webhook, subscription or event path, and its time-entries list has no modified-since filter,
only `date_from`/`date_to` and an `order_by` that includes `updated_at`. Toggl Track has
them, but a Free workspace gets one webhook of at most three events, only an admin may
create it, Toggl disables it after three failed deliveries (a machine that is asleep when the
event arrives is exactly that), it is delivered to one instance, and its payload is a raw
entry that would still need the Reports API for rounding and pricing — for the API being
migrated away from. The recorded follow-up, for Toggl 2.0 only, is a true delta:
`order_by=-updated_at` with a watermark would make a refresh one request regardless of
history; what stands in its way is confirming live whether deleted entries are listed with
`deleted_at` set, since otherwise a deletion would be seen only by the daily reconcile.
Fly's auto-stop is not a problem to solve here: the idle delay is not configurable, a
machine kept awake makes more requests rather than fewer, and the snapshot makes a wake
cost nothing.

**A month's income is the invoices that fell due the month before — plus the estimates
for the work no invoice covers yet.** Money earned in month M is invoiced and paid after
the hours are in, so the income shown for a month is exactly the invoices whose due date
fell in the previous month — `ComputeInvoiced` maps each due date to the month after it,
and `fundingIncome` reads that map. When reality deviates — an early-paying client, a
December invoice booked in January — the owner writes the month down per invoice in
`data/usable-invoices.json` (§3.6), and the written-down month wins.

**A worked month without its invoice yet is an estimated invoice.** The hours are tracked
in Toggl continuously, but the invoice for a worked month only comes into existence later
— the hours are compiled and billed the following month — so a model of invoices alone
would predict zero income for every month the bookkeeping has not caught up with. For
each income month I, `fundingIncome` therefore also counts the estimate for the worked
month I−2: the tracked hours the client's invoices do not already cover, plus the
workdays still unbilled after that, at the standard rate — the same tracked-plus-expected
arithmetic the work record panel prices, landing as income two months after the work,
where the real invoice will land once the hours are in. The estimate is modelled as an
invoice, not as direct labor income: the old two-month shift of the income *figure* stays
gone, the estimate simply enters the same due-month → income-month map. The dashboard
renders it as a grey row beside the real invoices, and the Income panel's total prices
only what no invoice exists for yet.

What the invoices already cover is decided per client by a **coverage frontier**: the
latest extent over the client's issued invoices, where an invoice without further
specification covers through the end of the month it was issued in, and the optional
`tracking_until` field on an invoice names the exact day its coverage ends, up to and
including it — the clean separation for clients charged more often than monthly. A
month the frontier covers whole contributes nothing; a month the frontier ends inside
contributes the workdays after it, priced by day, since the monthly aggregates cannot
price individual days. A worked month whose Toggl client no recipient links
(`tracking_client_id`, §3.2) is estimated anyway — and flagged on the work record panel
as a likely mistake, the fix being a linked recipient in the data repo — while a client
on `togglIgnoredClientIds` in config.json, the owner's own company among them, is
excluded from income and estimates outright. When Toggl cannot answer, the estimates
drop and the real invoices alone remain, with the usual error note.

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
represent. `fixed` is the one mode that ignores affordability: it is paid whether or not the money
is there. It is not, however, allowed below the minimum wage in force — outranking what
the company can afford is a choice, and outranking the law is not.

**A month is a closed system.** The payroll pot is the month's company income minus its
company expenses — both plannable: income from the invoices that fell due the month
before, expenses from budget.json's company categories. What a full salary can afford is
that pot; a full salary takes it all and the month closes at zero. Nothing carries in and
nothing carries out: there is no roll-forward, no opening balance, and no company balance
to feed the cascade. A reserve, when one is wanted, is an ordinary company expense
category in budget.json, planned like any other spend — the plan is the place for a
decision like that, not a special mechanism.

**An account keeps every reading, not just the last.** `accounts.json` used to hold one
`balance` and one `as_of` per account, so re-reading the bank wrote over the previous
figure — lossy in the one direction that matters, since the reason to read again is that
the carried figure has drifted. Readings live in a `balances` array now, and the one in
force for a month is the newest dated before it — the figure the **spending page** shows
with the account's name. The finance page reads none of it: its balance is derived from
net income and private expenses, never from the bank's own figure, so a reading feeds
reconciliation and nothing else. A later reading replaces the figure for every month
after it without rewriting what was true before, so paging back shows what was true then
rather than a number read a year later.

Two rules the schema cannot state are refused rather than guessed: two accounts sharing a
name — now two histories for one account, with nothing to merge them by, where the old file
silently summed them — and two readings in one month, which would be two candidate figures
for the month. A rejected file also says so on the page; switching the readings off in
silence is indistinguishable from having no `accounts.json` at all.

**Where a reading is explained, and where it is not.** The finance page shows no account
rows at all — it is a page of derived arithmetic, and a bank figure read off a file is
not derived from anything. An account appears on the **spending page**, name, figure and
read date together, next to Coverage: that is the page you are on while reconciling
against a statement, and so the page where learning the bank was last read six weeks ago
actually changes what you do. The `note` on a reading is documentation for whoever edits
`accounts.json` and renders nowhere.

**`targetBalance` used to exist and does not any more.** It held a month at the statutory
minimum until the company's carried balance reached a figure the config named — a floor
that fed on the roll-forward this model no longer has. It is removed entirely; a config
still carrying the block is refused. A reserve, when wanted, is an ordinary company
expense category: planned, visible, and editable with the rest of the budget.

`/info` shows
the dated blocks as a timeline — one card per month anything changes, resolving what is in
force from then — because an entry's own line rarely is: a legislation entry carries each
field it omits forward independently from the last entry that stated it, and a closed
salary span falls back to a full salary without saying so.

**Planned versus actual.** `budget.json` is what was planned; `actuals/YYYY-MM.json` is
what the bank statement said. Actuals move the **Actual column and nothing else**: every
planned figure of the month on screen is bit-identical with and without them, and a test
asserts that, because it is easy to break by accident and invisible when broken. The
pair is only meaningful side by side — the plan says what the month was meant to spend,
the statements say what actually left, and the gap is the spending the plan booked that
the bank has not seen yet.

A reserve that the plan makes no room for is exactly the case actuals are there to catch:
the statements show money leaving that the budget never planned, in the Actual column,
where it cannot be mistaken for part of the plan.


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

**An imported month has two columns.** The plan charges the whole month on the first, so
the plan and the statements answer different questions — what the month was meant to
spend, and what it actually spent. Both are shown, in the Planned-against-Actual columns
the ledger already uses. Mid-month that gap is most of the month; in a closed month it is
the overspend or the underspend. The panel closes on its summary, and the summary says
which side of that line the month is on: a closed month's balance weighs what was
actually spent, while the month in progress and every future one weigh the plan — half
a month of statements is not a month of spending, and the plan is the whole month's
intent. The bank's own reading for the month is on the spending page, not blended into
a figure that mixes the two.

Every statement line carries exactly one disposition: a budget category, an `ignored`
reason, an `untracked` note for money not yet decided, or `splits` when one line paid for
more than one thing. Absence is never a decision — the file accounts for every row on the
statement, so a skipped line is recorded as skipped rather than inferred from silence.

**What used to live here and does not any more.** Dividends (with company profit tax
and dividend tax), the director's loan, the `movement` markers on statement lines, the
company-worth figure, the `targetBalance` blocks, and the carried balances (roll-forward,
opening figures, the company balance as a cascade input) are all removed. The tracker
models the simple thing: plannable company income (invoices due the month before) and
company expenses define the earner's income, the private budget spends it, and the bank's
per-month readings live on the spending page. A deployment upgrading from a version that
had any of this must first drop the `dividends` array from `budget.json`, any `movement`
fields from its actuals, the `director_loan` block from `accounts.json`, the
`companyProfitTax` and `dividendTax` entries and any `targetBalance` block from
config.json — the validators refuse them all rather than silently ignoring them.

## 11. Agent-facing read API

`internal/api` is one service with two adapters: REST under `/api` and MCP at `/mcp`.
Both are gated by a bearer token, and when it is unset neither is registered at all — an
unauthenticated caller gets a 404 and learns nothing about the surface. The service knows
nothing of HTTP; the adapters translate and never compute, which is what stops the two
drifting apart.

**The surface is read-only.** Six tools: the budget categories, the plan for a month or a
year, a month's committed actuals, a description search over committed history, the
accounts with their newest reading dates, and the invoices. The search is deliberately
the substitute for a rules file: an agent asks how a merchant was treated last time and
gets the answer from what was actually recorded, rather than from a list that drifts out
of sync with it.

Writes used to exist here — statement lines added and re-attributed, balances recorded,
planned expenses moved, amounts scheduled to step, invoices drafted, issued and paid, and
whole data files replaced — each one audited as a commit to the data repo. They were
removed: the audit they provided was the argument for them, but the agent that wants them
is the same agent that can edit the data repo with git and get a better review than a
tool description can give. Nothing was lost that the data repo does not do better, and
the surface that remains needs no trust at all.

Reads answer from the data repo directly — through the same loaders the pages use, so a
figure this API reports and a figure on the page cannot drift. There is no read-after-
write overlay to reason about, no deploy lag between surfaces: what is committed is what
every reader sees. `docs/AGENT-API.md` is the contract an AI client works to, and the MCP tool
descriptions are where it actually reads it.

## 12. Uploads

The in-app chat is gone; it returns later as a standalone app. In its place sits the
**uploads flow** — the one place the app turns paper and exports into recorded data, and
the one deliberate exception to §11's read-only verdict, because the thing being written
is exactly what the data repo cannot produce by itself: the statement lines only the
administrator's phone or bank can supply.

**An inbox, a parser, a review, a commit.** `GET /uploads` is an administrator-only page.
`POST /uploads` takes several files at once — CSV exports, PDF statements, photos of
payment confirmations — and stores the originals untouched under `UPLOADS_DIR` (default
`<STATE_DIR>/uploads`, one `0700` directory per upload). Parsing runs in a background
goroutine per file — a statement photo can take minutes — and the page polls
`GET /uploads/status` and reloads itself when everything is done. A file the parser
cannot read stays in the inbox marked failed, with the model's complaint, and can be
retried. Nothing is written to `DATA_DIR`; the inbox is state, like the Toggl cache, and
`/info` counts it and can purge it.

**The parser is the chat's model client, minus the conversation.** The OpenAI-compatible
client lives in `internal/llm` — the same one-file rule as before (`openai.go` is the only
importer of the SDK), the same `OPENAI_*` configuration, the same streamed
chat-completions protocol against OpenAI or OpenRouter. What changed is the call's shape:
one system prompt carrying the actuals conventions — positive is money out, the date is
the booking date, the description is the statement line verbatim — and the budget's
category ids, then one user message holding the document: text for a CSV, a base64 data
URL for an image, a file part for a PDF. The answer must be a bare JSON array of
transactions. Lines the model will not vouch for come back with no category, and are
staged as untracked with a reason — an honest empty beats a confident wrong guess, and
the schema's "a category that no longer resolves fails validation loudly" rule makes a
hallucinated id a broken commit rather than a silent one.

**Nothing reaches the data repo without a person.** Parsed lines are staged, and the page
renders every one with a category dropdown — the budget's categories plus untracked — so
a wrong guess is corrected before anything is committed. Confirm reads the month's
`data/actuals/<month>.json` through the Contents client, merges, validates the merged
file with the same `ValidateActuals` the data repo's CI runs, and commits it back with a
message naming the upload. The ids are derived deterministically from
account+date+amount+description, so re-uploading the same statement is idempotent: the
second confirm finds every line already there and commits nothing. A statement that
spans a month boundary commits as two month files, because the schema demands it.
Without `GITHUB_DATA_TOKEN` the confirm button refuses — the same write gate the agent
API's Put path always had. This is the write path §11 removed, in its one legitimate
form: not the agent writing to the data repo it could edit anyway, but the app recording
what only a human's phone could capture.

## 13. What is built, and what is deliberately not

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

## 14. Settled questions

- **Number format.** Frozen as `10 200,00 €` — non-breaking space for thousands, comma
  for the decimal, symbol last. `render.formatMoney` is the only implementation and
  `TestFormatMoney` asserts the literal bytes, because "looks right" and "is right"
  diverge silently on an invisible U+00A0.