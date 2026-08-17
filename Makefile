.PHONY: build validate test vet fmt generate clean

# What the header shows. A plain `go build ./...` skips this and says "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/titaniumcoder/pocket-cfo/internal/buildinfo.Version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" ./...

validate:
	go run ./cmd/pocket-cfo-ctl validate

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
