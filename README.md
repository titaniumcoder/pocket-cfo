# PocketCFO

[![Build](https://github.com/titaniumcoder/pocket-cfo/actions/workflows/build.yml/badge.svg)](https://github.com/titaniumcoder/pocket-cfo/actions/workflows/build.yml)

A freelancer's finance tracker and invoicing tool in one app, originally built by
**Titanium Coder EOOD** for its own use, released here as the public half of a
two-repo split: this repo is the application code, MIT licensed; a private companion
repo (`pocket-cfo-data`) holds one particular business's real data (invoices,
recipients, budget, user access) and is what an actual deployment mounts into this
repo's published Docker image — see [`AGENTS.md`](AGENTS.md#releases) for how that
image gets built and tagged. Clone this repo on its own and it runs standalone against
the fabricated sample data checked into `data/` — no companion repo required to try
it out.

- **Finance tracker** (`/`, the landing page) — monthly income predictions off a
  configured hourly rate, optionally backed by real Toggl-tracked hours, automatically
  superseded by real invoiced income once an invoice for a linked client is issued; plus
  a hand-maintained expense budget.
- **Invoicing** (`/invoicing`) — one JSON file per invoice, GitHub Actions builds the
  PDFs, a read-only web app for viewing them.

Both parts share one login (GitHub OAuth for full/admin access, email-OTP for
per-part read-only access controlled by the private data repo's `users.json`) and one
top nav — see [`ARCHITECTURE.md`](ARCHITECTURE.md) for the invoicing side's full
design (data model, tax regime handling, rendering/signing pipeline, build order); the
finance tracker's own conventions are documented inline in `internal/finance`. This
README is deliberately short and points there instead of duplicating it.

## Status

Directory layout, JSON Schemas, invoice rendering (`internal/money`, `internal/render`,
`pocket-cfo-ctl render`, original + `-paid.pdf` artifacts), a GitHub Actions build/test
pipeline (`.github/workflows/build.yml`), conventional-commit-driven releases (cut locally
via the `release-it` skill, which triggers `.github/workflows/release.yml`'s image publish
on tag push — see [`AGENTS.md`](AGENTS.md#releases)), and a GitHub-OAuth-gated web app
(`cmd/pocketcfo`) covering both the finance tracker and invoicing are in place. PDF
signing (`internal/sign`, DocMDP-certified via `digitorus/pdfsign`) is implemented but
inert — no signing secrets are configured yet.

## Prerequisites

- Go 1.26 or later (matches `go.mod`; uses the `tool` directive, available since Go
  1.24, for dev-time tooling).
- An [api2pdf](https://www.api2pdf.com/) API key, for `pocket-cfo-ctl render`.
- A GitHub OAuth App (Settings → Developer settings → OAuth Apps), for `cmd/pocketcfo`'s
  login. Callback URL must match `PUBLIC_BASE_URL` below plus `/auth/callback`.
- A [Toggl Track](https://toggl.com/track/) account (optional) — `TOGGL_API_TOKEN`/
  `TOGGL_WORKSPACE_ID` unset just leaves the finance tracker's tracked-hours layer
  disabled; predictions still run off the configured hourly rate.
- [direnv](https://direnv.net/) (optional) to auto-load `.envrc`.
- [air](https://github.com/air-verse/air) (optional), for hot-reloading `cmd/pocketcfo`
  while developing — install it yourself (`go install github.com/air-verse/air@latest`),
  it's a dev-time tool, not a module dependency.

## Configuration

Copy `.envrc.example` to `.envrc` and fill in the real values — `.envrc` is gitignored,
`.envrc.example` isn't. With direnv installed, `cd` into the repo loads it automatically;
otherwise `source .envrc` before running `pocket-cfo-ctl render` or `cmd/pocketcfo`.

| Variable | Used by | |
|---|---|---|
| `ENV` | `cmd/pocketcfo` | GitHub OAuth login is only enforced when this is exactly `prod` — unset/anything else skips auth entirely, so local dev needs none of the `GITHUB_OAUTH_*`/`SESSION_SECRET`/`PUBLIC_BASE_URL` vars below. A real deployment sets it to `prod`. |
| `API2PDF_KEY` | `pocket-cfo-ctl render` | api2pdf API key |
| `HERMES_API_TOKEN` | `cmd/pocketcfo` | optional — bearer token for the Hermes API. Unset means `/api/` is never registered, so the endpoints don't exist rather than returning 401 |
| `GITHUB_DATA_TOKEN` | `cmd/pocketcfo` | optional — fine-grained PAT with `contents: write` on the data repo only, for writing reconciled months back as commits |
| `SIGN_CERT_B64` / `SIGN_KEY_B64` / `SIGN_KEY_PASS` | `pocket-cfo-ctl render` | base64 PEM cert/key `pocket-cfo-ctl render` certifies each PDF with; unset skips signing entirely (see `make dev-cert` below and ARCHITECTURE.md §6) |
| `GITHUB_OAUTH_CLIENT_ID` / `_SECRET` | `cmd/pocketcfo`, prod only | the GitHub OAuth App above |
| `SESSION_SECRET` | `cmd/pocketcfo`, prod only | any random string; encrypts the session cookie |
| `PUBLIC_BASE_URL` | `cmd/pocketcfo`, prod only | `http://localhost:8080` locally, the deployed URL in prod |
| `GITHUB_REPO` | `cmd/pocketcfo`, prod only | `owner/repo` collaborator permission is checked against |
| `OTP_LINK_SECRET` | `cmd/pocketcfo`, prod only | any random string; signs the self-expiring `/auth/email` login link |
| `DATA_DIR` | `cmd/pocketcfo`/`pocket-cfo-ctl`, optional | default `data` — recipients/, invoices/, users.json, budget.json all live under here; override to point at a data checkout outside this repo (e.g. the mounted private data repo in a real deployment). Who (beyond GitHub collaborators, always full admins) may reach which part(s) via the email-OTP login is `$DATA_DIR/users.json`; see `internal/users`, `schemas/users.json` |
| `AWS_REGION` / `SES_FROM_EMAIL` | `cmd/pocketcfo`, prod only | Amazon SES sends the `/auth/email` login link; `SES_FROM_EMAIL` must be a verified SES identity, unset logs the link instead of emailing it, for local testing |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `cmd/pocketcfo`, prod only | AWS credentials for SES, read by the AWS SDK's default credential chain (not by this app's own config) — needs only `ses:SendEmail` on the `SES_FROM_EMAIL` identity, see ARCHITECTURE.md §8 |
| `TOGGL_API_TOKEN` / `TOGGL_WORKSPACE_ID` | `cmd/pocketcfo`, optional | Toggl credentials for the finance tracker's tracked-hours layer; unset leaves it disabled, predictions still run off the configured hourly rate |
| `TOGGL_REFRESH_INTERVAL` | `cmd/pocketcfo`, optional | default `15m` — how often the current year's Toggl data is refreshed in the background so page requests serve from cache instead of waiting on the API (see `tracker.Tracker.Warm`). A Go duration, e.g. `5m`; an unparseable or non-positive value logs a warning and falls back to the default |
| `CONFIG_FILE` | `cmd/pocketcfo`, optional | default `config.json` — non-secret finance tunables: `hourlyRateCents`, `currency`, `hoursPerDay`, `annualVacationDays`, plus the three dated blocks below. See `internal/finance/config`, whose `FileConfig` doc comment is the reference for each |
| `BUILD_DIR` | `cmd/pocketcfo`/`pocket-cfo-ctl` | optional, default `build` — rendered PDFs (generated output, kept separate from `DATA_DIR`'s hand-edited data) |
| `TEMPLATES_DIR` / `STATIC_DIR` | `cmd/pocketcfo`; `TEMPLATES_DIR` also `pocket-cfo-ctl render` | optional, default to `templates` / `static`; override for a deployment with its own branding |

### `config.json`'s dated blocks

Three settings are lists of dated entries rather than single values, because the thing
they describe changes on a date and last year's figures have to stay reproducible. Each
entry states only what changed; anything it omits carries forward.

| Block | What it decides |
|---|---|
| `legislation` | Every government-set payroll figure — both parties' contribution schedules as marginal bands, the income tax bands, the minimum wage. There are no built-in defaults: a figure no entry states is zero, and the page says so rather than inventing a plausible rate. |
| `salary` | Whether a month pays a full salary, only the statutory minimum, or none at all. Absent means every month is full. |
| `startMonth` | The first month budgeting covers. Earlier months are not offered at all, and the year it falls in is aggregated from it rather than from January. |

```json
{
  "startMonth": "2026-04",
  "legislation": [
    { "from": "2026-01",
      "contributions": {
        "employer": { "bands": [ {"from": 0, "rate": 0.1892}, {"from": 2112, "rate": 0} ] },
        "employee": { "bands": [ {"from": 0, "rate": 0.1378}, {"from": 2112, "rate": 0} ] }
      },
      "incomeTax": { "bands": [ {"from": 0, "rate": 0.10} ] } },
    { "from": "2026-07", "minimumWage": 1077 }
  ],
  "salary": [ { "from": "2026-09", "to": "2026-10", "mode": "minimum" } ]
}
```

Bands are marginal — a rate applies only to the slice of the base inside its own band —
which is why a contribution ceiling is an ordinary band with a rate of `0` rather than a
field of its own. A malformed entry is a startup failure, not a warning: these are legal
obligations, and a typo that silently disables one is the failure the setting exists to
prevent. `/info` renders both blocks back as one line each, to be compared against the
file by eye.

## Building and running

**Run `make generate` first in a fresh clone.** The Go types generated from
`schemas/*.json` are build output rather than source, so they aren't checked in and
nothing compiles until they exist. Re-run it whenever a schema changes.

```
make generate   # regenerate the schema-derived Go types (do this first)
make build      # go build ./...
make test       # go test ./...
make vet        # go vet ./...
make fmt        # gofmt -l -w .
make clean      # go clean ./...
make dev-cert   # print a throwaway self-signed SIGN_CERT_B64/SIGN_KEY_B64 pair for local testing

go run ./cmd/pocketcfo                                 # run the web app (finance tracker + invoicing)
go run ./cmd/pocket-cfo-ctl render                     # render every invoice under data/invoices
go run ./cmd/pocket-cfo-ctl render INV-0000000001      # render just one
go run ./cmd/pocket-cfo-ctl render --dry-run           # show what would render, without rendering
go run ./cmd/pocket-cfo-ctl render INV-0000000001 --force  # re-render even an already-issued PDF
                                                        # (--force requires a single invoice number)

air                                             # hot-reload cmd/pocketcfo on file changes,

docker build -t pocket-cfo .                    # build the release image locally
docker run -p 8080:8080 pocket-cfo              # run it standalone against the fabricated
                                                 # sample data baked into the image

docker pull ghcr.io/titaniumcoder/pocket-cfo:latest   # or pull the published image instead
```

For a real deployment, mount a real data checkout over `/app/data` (or point the
`*_DIR`/`*_FILE` env vars at it individually) rather than rebuilding the image — see the
Dockerfile and [`AGENTS.md`](AGENTS.md#releases) for how released images get published.

Prebuilt `pocketcfo`/`pocket-cfo-ctl` binaries for Linux, macOS (Intel and Apple Silicon), and
Windows are attached directly to each [GitHub
Release](https://github.com/titaniumcoder/pocket-cfo/releases) — no Docker or Go toolchain
required, just download and run.

## License

[MIT](LICENSE).
