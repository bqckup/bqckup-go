.PHONY: fmt vet test build verify

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

vet:
	go vet ./...

test:
	go test -race ./...

build:
	go build ./cmd/bqckup

verify: fmt vet test build
