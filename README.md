# PocketCFO

[![Build](https://github.com/titaniumcoder/pocket-cfo/actions/workflows/build.yml/badge.svg)](https://github.com/titaniumcoder/pocket-cfo/actions/workflows/build.yml)

A freelancer's finance tracker and invoicing tool in one Go binary, MIT licensed.

- **Finance tracker** (`/`) — income predicted from an hourly rate, optionally from real
  Toggl-tracked hours, superseded by real invoiced income once an invoice is issued; a
  planned expense budget; and the money actually spent, reconciled from bank statements
  and shown beside the plan.
- **Invoicing** (`/invoicing`) — one JSON file per invoice, PDFs rendered by CI, a
  read-only viewer, and per-client portal links.
- **An agent-facing write API** (`/api`, `/mcp`) — so a reconciliation agent can record
  and correct transactions without a shell. Every accepted write is a git commit; see
  [`docs/HERMES.md`](docs/HERMES.md).

Clone it, `make generate`, `go run ./cmd/pocketcfo`, and it serves the fabricated sample
data in `data/`. That is enough to look around, and not enough to run a business — for
that you want your own data repo, which is what most of this README is about.

## The two-repo split

**This repo is code and sample data. Your real data belongs in a second, private repo.**

Nothing here is coupled to one business: the app reads its data from directories chosen
by environment variables, and a released image with your data copied over the sample data
is a complete deployment.

```
titaniumcoder/pocket-cfo            your-org/your-data  (private)
├── cmd/, internal/                 ├── data/           ← your invoices, budget, actuals
├── templates/, static/             ├── build/          ← rendered PDFs, committed by CI
├── data/        (samples)          ├── config.json     ← your rates and payroll law
└── Dockerfile   (samples baked)    ├── Dockerfile      ← FROM the published image
                                    └── .github/workflows/
```

`titaniumcoder/pocket-cfo-data` is the reference implementation of the right-hand side. It
holds no application code at all — only data, a validate-and-render pipeline, and a
Dependabot config that opens a pull request whenever this repo publishes a new image tag.

### Setting up your data repo

**1. Copy `data/` as a starting point.** It has one of everything, and each file's shape is
enforced by a schema. Keep the layout: the app's defaults point at these exact paths.

| Path | What it is | Schema |
|---|---|---|
| `data/issuer.json` | your business's legal and bank details | `schemas/issuer.json` |
| `data/recipients/NNNN.json` | one per client | `schemas/recipient.json` |
| `data/invoices/INV-*.json` | one per invoice; never edited after issue | `schemas/invoice.json` |
| `data/paid-invoices.json` | which invoices are paid, and when | `schemas/paid-invoices.json` |
| `data/users.json` | who may read which part, by email | `schemas/users.json` |
| `data/budget.json` | planned expenses; every category has a stable UUID | `internal/finance/data/budget.schema.json` |
| `data/actuals/YYYY-MM.json` | what was actually spent, per month | `internal/finance/data/actuals.schema.json` |
| `data/accounts.json` | real account balances, read at month end; every reading is kept, and each account declares `kind` — `company` or `private` | `internal/finance/data/accounts.schema.json` |
| `config.json` | non-secret tunables and payroll law | `internal/finance/config` |

Payment lives in `data/paid-invoices.json` rather than in the invoice because an issued invoice
is never edited again, and budget categories carry UUIDs rather than names so that renaming
one does not orphan every transaction that cites it.

**2. Write a Dockerfile starting from the published image**, copying your data over the
samples. Every `*_DIR`/`*_FILE` default already points at these paths, so no env var
overrides are needed:

```dockerfile
FROM ghcr.io/titaniumcoder/pocket-cfo:v0.17.0
WORKDIR /app
COPY data ./data
COPY build ./build
COPY config.json ./config.json
```

Point Dependabot at that `FROM` line and new releases arrive as pull requests.

**3. Set the secrets** your deployment needs — see [Configuration](#configuration).
Everything non-secret lives in `config.json` in your repo; everything secret lives in your
host's environment and in neither repo.

**4. Give CI the pipeline.** Four stages in order, each of them this repo's
`pocket-cfo-ctl` doing the work: translate missing invoice text, validate against the
schemas and the business rules, render any invoice with no PDF yet, then deploy. Pull
requests run validation alone — nothing on a branch translates, renders, commits or ships.
`pocket-cfo-ctl` comes from a GitHub Release rather than a source build, so CI needs no Go
toolchain. `cmd/pocket-cfo-ctl` has the subcommands; `pocket-cfo-data`'s
`.github/workflows/data-pipeline.yml` is a working copy.

**5. For local work**, run this repo's binary against your data instead of committing to
see a change. Point `DATA_DIR`, `BUILD_DIR` and `CONFIG_FILE` at your checkout, or run from
inside it so the defaults resolve there — `pocket-cfo-data`'s `run.sh` is exactly that, in
a script.

### What the app never does

It does not write to `DATA_DIR`. A deployment's data directory is a baked image layer or a
mount, so a write landing there would be lost on restart and diverge from the repo.
Everything the agent API accepts is committed through the GitHub Contents API instead, and
the pipeline rebuilds. That is what makes an agent-facing write surface tolerable: it is
not trusted, it is audited, and every change is one you can read in `git log` and revert.

## Customizing the look

Three separate surfaces. None requires forking this repo — point the relevant environment
variable at your own directory and the defaults are ignored.

### 1. Web pages — `templates/`, overridden by `TEMPLATES_DIR`

| File | Page | Handler |
|---|---|---|
| `templates/index.html` | invoice list at `/invoicing` | `handleIndex`, `cmd/pocketcfo/main.go` |
| `templates/info.html` | diagnostics at `/info` | `handleInfo`, `cmd/pocketcfo/info.go` |
| `templates/client.html` | client portal at `/invoicing/client/{token}` | `handleClientPortal`, `cmd/pocketcfo/client.go` |

Go `html/template` files. The data each receives is the struct its handler passes — read
the handler rather than guessing, and note the field set *is* the contract: a template
reaching for a field the handler does not set fails at render.

All pages share one header, defined once in `internal/webui` and parsed into each template
ahead of the file itself, so a template can use it without redefining it. Change that and
the nav changes on every page at once.

### 2. Finance pages — `internal/finance/tracker/render.go`

The dashboard and the spending page are the exception: their templates are Go string
constants in that file rather than files under `templates/`. They are the only pages that
render bank-statement descriptions, and keeping them in the package is what stops that data
reaching any other template. Changing them means editing that file and rebuilding.

If what you want is different *figures* rather than a different layout, you want
`config.json` and `data/budget.json` instead.

### 3. The invoice PDF — `templates/invoice.html.tmpl`

One template renders every invoice in every language, turned into a PDF by
`internal/render`. Two things are deliberately *not* in it:

- **Language strings** live in `internal/render/labels.go`, one struct per language, so
  adding a language is adding an entry rather than copying the template.
- **Tax and legal notes** are chosen at render time by `internal/tax` from the issuer's and
  recipient's countries, against the catalog in `catalog/notes.json`. That resolver, not
  the template, decides what an invoice says about VAT — see ARCHITECTURE.md §4.

The web stylesheet is `static/app.css`, overridden by `STATIC_DIR`: one file for every
page, by design. The invoice PDF carries its own styles inline instead, because it is
rendered by an external service that cannot fetch your stylesheet.

## Configuration

Secrets and deployment-specific paths come from the environment. Copy `.envrc.example` to
`.envrc` — gitignored — and fill it in; direnv loads it on `cd`, or source it yourself.

| Variable | Used by | |
|---|---|---|
| `ENV` | `cmd/pocketcfo` | login is enforced only when this is exactly `prod`; anything else skips auth, so local dev needs none of the OAuth vars |
| `API2PDF_KEY` | `pocket-cfo-ctl render` | api2pdf API key |
| `HERMES_API_TOKEN` | `cmd/pocketcfo` | optional — bearer token for the agent API. Unset means `/api/` and `/mcp` are never registered, so they do not exist rather than returning 401 |
| `GITHUB_DATA_TOKEN` | `cmd/pocketcfo` | optional — fine-grained PAT with `contents: write` on the data repo only, for committing reconciled months |
| `GITHUB_OAUTH_CLIENT_ID` / `_SECRET` | `cmd/pocketcfo`, prod | the GitHub OAuth App; its callback is `PUBLIC_BASE_URL` + `/auth/callback` |
| `SESSION_SECRET` | `cmd/pocketcfo`, prod | any random string; encrypts the session cookie |
| `PUBLIC_BASE_URL` | `cmd/pocketcfo`, prod | the deployed URL |
| `GITHUB_REPO` | `cmd/pocketcfo`, prod | `owner/repo` whose collaborators get full access |
| `OTP_LINK_SECRET` | `cmd/pocketcfo`, prod | any random string; signs the email login link |
| `AWS_REGION` / `SES_FROM_EMAIL` | `cmd/pocketcfo`, prod | SES sends the login link; unset logs it instead, for local testing |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `cmd/pocketcfo`, prod | read by the AWS SDK's own credential chain; needs only `ses:SendEmail` |
| `TOGGL_API_TOKEN` / `TOGGL_WORKSPACE_ID` | `cmd/pocketcfo`, optional | unset leaves the tracked-hours layer disabled; predictions still run off the hourly rate |
| `TOGGL_REFRESH_INTERVAL` | `cmd/pocketcfo`, optional | default `15m`; a Go duration |
| `DATA_DIR` | both binaries, optional | default `data` |
| `BUILD_DIR` | both binaries, optional | default `build` — rendered PDFs, kept apart from hand-edited data |
| `CONFIG_FILE` | `cmd/pocketcfo`, optional | default `config.json` |
| `TEMPLATES_DIR` / `STATIC_DIR` | `cmd/pocketcfo`; `TEMPLATES_DIR` also for `render` | default `templates` / `static` |

### `config.json`

Non-secret, and it lives in *your* data repo. Beyond the scalars — `hourlyRateCents`,
`currency`, `hoursPerDay`, `annualVacationDays` — four settings are lists of dated
entries, because what they describe changes on a date and last year's figures have to stay
reproducible. Each entry states only what changed; anything it omits carries forward.

| Block | Decides |
|---|---|
| `legislation` | every government-set payroll figure: both parties' contribution schedules as marginal bands, the income tax bands, the minimum wage |
| `salary` | what a month pays: a full salary, only the statutory minimum, a `fixed` gross `amount`, or none at all |
| `targetBalance` | a figure the company saves towards: under it a month pays the minimum, at or above it full salary resumes |
| `startMonth` | the first month budgeting covers; earlier months are not offered at all |

`mode: "fixed"` takes an `amount` — a gross monthly figure — and is the one setting paid
whether or not the company can afford it, so it will overdraw the company rather than
shrink. It is still refused below the minimum wage in force.

`targetBalance` is a **floor**: once reached, a full salary is drawn out of what sits *above*
the target, so the reserve is not spent back down the following month. It only ever holds a
month back, never makes one pay more, so it does nothing in a month whose `salary` entry
already says `minimum`, `fixed` or `none` — that is allowed rather than refused, and `/info`
names every month where it is idle. It also needs an account with `"kind": "company"` in
`accounts.json`, since otherwise there is no balance to compare it against.

Bands are marginal, so a contribution ceiling is an ordinary band with a rate of `0` rather
than a concept of its own. There are no built-in defaults: a figure no entry states is
zero, and the page says so rather than inventing a plausible rate. A malformed entry stops
the app from starting — these are legal obligations, and a typo that silently disables one
is the failure the setting exists to prevent.

`internal/finance/config`'s `FileConfig` is the reference for every field, and `/info`
renders the parsed result back so it can be compared against the file by eye.

## Building and running

**Run `make generate` first in a fresh clone.** The Go types generated from the schemas are
build output, not source, so nothing compiles until they exist.

```
make generate   # regenerate schema-derived Go types (do this first)
make build      # go build ./...
make test       # go test ./...
make vet        # go vet ./...
make fmt        # gofmt -l -w .

go run ./cmd/pocketcfo                                # the web app
go run ./cmd/pocket-cfo-ctl render                    # render every invoice
go run ./cmd/pocket-cfo-ctl render INV-0000000001     # render one
go run ./cmd/pocket-cfo-ctl render --dry-run          # show what would render
go run ./cmd/pocket-cfo-ctl actuals validate          # check the recorded months
air                                                   # hot reload
```

Prebuilt binaries for Linux, macOS and Windows are attached to every
[release](https://github.com/titaniumcoder/pocket-cfo/releases), and the image is
`ghcr.io/titaniumcoder/pocket-cfo`.

## Further reading

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — the design: data model, tax regimes, rendering,
  the finance tracker, the agent API.
- [`docs/HERMES.md`](docs/HERMES.md) — the contract a reconciliation agent works to.
- [`AGENTS.md`](AGENTS.md) — conventions, and how releases are cut.

## License

[MIT](LICENSE).
