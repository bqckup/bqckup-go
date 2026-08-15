.PHONY: fmt vet test build install setup verify

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

vet:
	go vet ./...

test:
	go test -race ./...

build:
	go build -o bqckup ./cmd/bqckup

install: build
	install -d -m 0755 /usr/local/bin
	install -m 0755 bqckup /usr/local/bin/bqckup

setup:
	./scripts/install.sh

verify: fmt vet test build
