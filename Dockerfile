ARG GO_VERSION=1
FROM golang:${GO_VERSION}-bookworm as builder

WORKDIR /usr/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go generate ./...
# The version shown in the header. Passed by release.yml as the pushed tag;
# an unstamped build says "dev", which is what a local `go build` produces.
ARG VERSION=dev
RUN go build -ldflags "-X github.com/titaniumcoder/pocket-cfo/internal/buildinfo.Version=${VERSION}" \
      -o /out/pocketcfo ./cmd/pocketcfo
RUN go build -o /out/pocket-cfo-ctl ./cmd/pocket-cfo-ctl

FROM debian:bookworm as runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
# Two executables: pocketcfo (the web server, the default CMD below) and
# pocket-cfo-ctl (the invoicing CLI — render/validate/etc., run ad hoc against
# the same mounted data, e.g. `docker run --rm -v ./data:/app/data <image>
# pocket-cfo-ctl validate data`). One image, both entrypoints.
COPY --from=builder /out/pocketcfo /usr/local/bin/pocketcfo
COPY --from=builder /out/pocket-cfo-ctl /usr/local/bin/pocket-cfo-ctl
COPY --from=builder /usr/src/app/templates ./templates
COPY --from=builder /usr/src/app/static ./static
COPY --from=builder /usr/src/app/config.json ./config.json
# The note catalog the invoice validators check the mandatory wording against
# (ARCHITECTURE.md §4.2) — accountant-owned text that ships with the app like
# templates/ and static/ do. Without it, save_draft_invoice refuses every
# upload: a wording check that cannot run is refused, never skipped. A
# deployment with its own catalog points CATALOG_DIR at it or copies over
# this path (see README).
COPY --from=builder /usr/src/app/catalog ./catalog
# ./data/ (recipients/, invoices/, issuer.json, users.json, budget.json) and
# ./build/ (rendered PDFs) ship this repo's own fabricated sample data, so
# the image runs standalone out of the box -- try it with no setup at all.
# For a real deployment, bind-mount (or volume-mount) a real data checkout
# over one or both of these paths (or override DATA_DIR/BUILD_DIR/
# CONFIG_FILE individually to point elsewhere) -- none of this is baked into
# the binary itself, see internal/finance/tracker.Budget and
# internal/stats.LoadRecipients/LoadInvoices, all read fresh from disk on
# every request/check.
#   docker run -v /path/to/real/data:/app/data ... pocketcfo
COPY --from=builder /usr/src/app/data ./data
COPY --from=builder /usr/src/app/build ./build
# Which data checkout is mounted, shown under the title. The data is
# bind-mounted at run time (see above), so only whoever deploys knows its date
# and commit — set these there. Unset renders nothing at all.
ENV DATA_UPDATED_AT=""
ENV DATA_COMMIT=""

CMD ["pocketcfo"]
