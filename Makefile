.PHONY: build test vet fmt generate clean

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
