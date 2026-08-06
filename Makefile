.PHONY: build test vet fmt generate clean dev-cert

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

generate:
	go generate ./...

clean:
	go clean ./...

# dev-cert prints a throwaway self-signed EC cert/key (7-day validity) as
# export lines for local SIGN_CERT_B64/SIGN_KEY_B64 testing — see
# internal/sign and ARCHITECTURE.md §6. Nothing is written to disk. This is
# never the real org certificate: paste the output into .envrc for a local
# smoke test, then discard it.
dev-cert:
	@tmp=$$(mktemp -d); \
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
		-keyout "$$tmp/key.pem" -out "$$tmp/cert.pem" -days 7 \
		-subj "/CN=invoicer dev signer" >/dev/null 2>&1; \
	echo "# throwaway self-signed dev cert — 7 days, never the real org cert"; \
	echo "export SIGN_CERT_B64=$$(base64 < "$$tmp/cert.pem" | tr -d '\n')"; \
	echo "export SIGN_KEY_B64=$$(base64 < "$$tmp/key.pem" | tr -d '\n')"; \
	rm -rf "$$tmp"
