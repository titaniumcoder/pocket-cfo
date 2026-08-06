ARG GO_VERSION=1
FROM golang:${GO_VERSION}-bookworm as builder

WORKDIR /usr/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go generate ./...
RUN go build -o /out/pocketcfo ./cmd/pocketcfo

FROM debian:bookworm as runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /out/pocketcfo /usr/local/bin/pocketcfo
COPY --from=builder /usr/src/app/web ./web
COPY --from=builder /usr/src/app/config.json ./config.json
# ./data/ (recipients/, invoices/, issuer.json, users.json, budget.json) and
# ./build/ (rendered PDFs) ship this repo's own fabricated sample data, so
# the image runs standalone out of the box -- try it with no setup at all.
# For a real deployment, bind-mount (or volume-mount) a real data checkout
# over one or both of these paths (or override RECIPIENTS_DIR/INVOICES_DIR/
# BUILD_DIR/USERS_FILE/BUDGET_DIR/CONFIG_FILE individually to point
# elsewhere) -- none of this is baked into the binary itself, see
# internal/finance/tracker.Budget and internal/stats.LoadRecipients/
# LoadInvoices, all read fresh from disk on every request/check.
#   docker run -v /path/to/real/data:/app/data ... pocketcfo
COPY --from=builder /usr/src/app/data ./data
COPY --from=builder /usr/src/app/build ./build
CMD ["pocketcfo"]
