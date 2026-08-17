.PHONY: build validate test vet fmt generate clean

build:
	go build ./...

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
