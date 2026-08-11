# PocketCFO — Architecture

Invoicing for **Titanium Coder EOOD** (Varna, Bulgaria). Volume: 1–3 invoices/month.

**One private repo. One JSON file per invoice. GitHub Actions builds the PDFs. The web
app is a read-only viewer.** Data is edited by hand and committed.

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
4. **Tamper evidence is the digital signature**, not a hash field. For the JSON side,
   `git log -p` on one file is the audit trail.
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
  pocketcfo/         web app (finance tracker + read-only invoicing)
  pocket-cfo-ctl/    CLI — the whole pipeline, identical locally and in CI
internal/
  schema/ 
  tax/ 
  money/ 
  render/ 
  sign/ 
  stats/ 
  translate/ 
  auth/ 
  gitts/
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

Note what is *not* checked: whether an issued invoice's content changed. The signature on
the PDF is the tamper evidence, and `git log -p data/invoices/INV-….json` is the record.
A check here would be redundant.

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
| `INV-….-draft.pdf` | Render if status is `draft`. Overwrite on each run of the action. |
| `INV-….-paid.pdf` | Render if the invoice is listed in `paid-invoices.json` and it's missing, or if the JSON is newer. |

"Newer" means per-file git commit timestamps:

```
git log -1 --format=%ct -- data/invoices/INV-0000000002.json
git log -1 --format=%ct -- build/INV-0000000002-paid.pdf
```

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

Secrets: `API2PDF_KEY`, `DEEPL_API_KEY`, `SIGN_CERT_B64`, `SIGN_KEY_B64`,
`SIGN_KEY_PASS`. Local runs use the same values from a `.env`; there is no CI-only step.

Translation stays a separate manual-dispatch workflow that opens a PR. Machine
translation into a language you can't proofread shouldn't land unreviewed on a legal
document, and at this volume running it by hand costs nothing.

---

## 6. Rendering & signing

```go
type Renderer interface { Render(ctx context.Context, html []byte) ([]byte, error) }
type Signer   interface { Sign(ctx context.Context, pdf []byte) ([]byte, error) }
```

**HTML** — Go `html/template`, one canonical layout matching the reference PDFs.

**PDF — api2pdf.** `POST https://v2.api2pdf.com/chrome/pdf/html`, key in the
`Authorization` header. Use the **Chrome** endpoint, not wkhtmltopdf — Cyrillic shaping
is where wkhtmltopdf falls over. `outputBinary: true` to get bytes back inline and skip
their 24-hour file store. Retry with backoff on 5xx.

**Fonts — Google Fonts, with two required settings.**

```html
<link href="https://fonts.googleapis.com/css2?family=Noto+Sans:wght@400;700&display=block&subset=cyrillic,latin-ext" rel="stylesheet">
```

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

**Signing — local, not api2pdf.** api2pdf's service groups (Chrome, wkhtmltopdf,
LibreOffice, Markitdown, OpenDataLoader, PdfSharp, Zip, Zebra) cover merge, password,
bookmarks and page extraction; there is no signing endpoint.

- `digitorus/pdfsign`, pure Go library, runs inside the Action. Sign all three artifacts.
- Org certificate → integrity and provenance. Enough unless a client demands qualified
  status, which needs a QES cert from a Bulgarian QTSP (B-Trust / Evrotrust /
  InfoNotary). Defer it.
- Since the signature is now the *only* tamper evidence on the PDF side, move signing
  earlier than you otherwise would — it's a small library, not a service.
- **Let's Encrypt does not work for this.** It only issues domain-validated TLS server
  certs (`serverAuth` EKU); its CA policy has no document-signing/S-MIME product, and
  even a borrowed TLS cert would be rejected by viewers that check EKU before trusting a
  PDF signature.

**Status: implemented, inert.** `internal/sign.PDFSigner` wraps `digitorus/pdfsign`:
`CertType: CertificationSignature` + `DocMDPPerm: DoNotAllowAnyChangesPerms` (any edit
after signing breaks the signature in every conforming viewer — that's the
"unchangeable" mechanism, not encryption or ACLs), plus an RFC3161 timestamp (default
`https://freetsa.org/tsr`) so validity survives the signing cert's own expiry.
`pocket-cfo-ctl render` signs every PDF it writes whenever `SIGN_CERT_B64` /
`SIGN_KEY_B64` (+ optional `SIGN_KEY_PASS` for a password-encrypted key, all base64 PEM)
are set — and is a complete no-op otherwise, same convention as `API2PDF_KEY`. No repo
secrets are set today, so CI keeps rendering unsigned. `make dev-cert` prints a
throwaway self-signed pair for local testing. The real cert is still a B-Trust QES,
once a client actually requires a signed document — swapping it in is a secrets change,
not a code change.

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

## 8. Web app — read-only viewer

**Current state**: reads `data/`, `build/` from local disk (`DATA_DIR`/
`BUILD_DIR`, see §2), same as `pocket-cfo-ctl`. No DB.

**Planned**: stateless — no disk at all, reading `build/index.json`, `build/stats.json`
and the PDFs from GitHub via the Contents API instead, cached in memory with an ETag and
a short TTL. Not yet built.

Auth: GitHub OAuth App, scopes `read:user repo`. On callback,
`GET /repos/{owner}/{repo}/collaborators/{login}/permission`; require `push` or `admin`.
Cache in an encrypted session cookie, 10-minute TTL. Everything behind auth — there is no
public route.

**Second, lesser-trust path: email login.** Anyone on a fixed allowlist
(`OTP_ALLOWED_EMAILS`) can request a login link at `/auth/email` — a signed,
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
account is still in the sandbox, every recipient in `OTP_ALLOWED_EMAILS` —
not just `SES_FROM_EMAIL` — must also be a verified identity, or production
access must be requested first.

Deploy: single Go binary, any host, scale-to-zero fine — no local state.

**Later, if hand-editing gets tiresome**: `POST /invoices/{n}/paid` appending to
`data/paid-invoices.json` through the GitHub Contents API using the visitor's own OAuth
token. Splitting payment out of the invoice document is what makes this a small, additive
write rather than a rewrite of an issued invoice.
No database appears, the commit is attributed to the actual person, and GitHub enforces
write permission for you. That's the only write path worth adding — one-click payment
marking is what keeps the statistics honest.

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

The shallow-clone case deserves a real test. If `fetch-depth` is ever wrong, every
timestamp comes back empty and the build quietly regenerates every `-paid.pdf` — which
costs money and churns the repo without failing.

---

## 10. Build order

0. **Font spike.** Google Fonts link with `display=block`, `delay: 1500`, POST hardcoded
   Cyrillic + German HTML to api2pdf, look at the PDF. 20 minutes, de-risks the thing
   most likely to eat an afternoon.
1. Schema + `money` (discounts, VAT grouping) + `pocket-cfo-ctl validate`, with the totals
   test against both references.
2. `tax.Resolve` + catalog + the §4.3 validators. **Get this right before rendering.**
3. Template; reproduce both references exactly. Golden HTML tests green.
4. `pocket-cfo-ctl render` → api2pdf → `build/`, with the §5.1 rules, `--force`, `--dry-run`.
5. `build.yml`.
6. Signing — earlier than you'd think, since it's the only tamper evidence.
7. `pocket-cfo-ctl index`; app: list, detail, PDF download.
8. Payment (`data/paid-invoices.json`): validators, `-paid.pdf`, stats, dashboard.
9. Annulment, when the first one actually happens — this is what restores §3.7:
   schema, validators, `-ANUL.pdf`, stats exclusion. Confirm the
   annulment-vs-credit-note split with the accountant then.
10. `pocket-cfo-ctl new` scaffolding — by then you'll know what you want it to fill in.
11. DeepL as a PR-opening workflow.
12. VIES + monthly reports.

Steps 1–8 are the system. Everything after is additive.

---

## 11. Open questions

- Number format: `1.000,00 €` or `10 200,00 €` — pick one and freeze it.