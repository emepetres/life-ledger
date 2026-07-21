# Life Ledger — single static binary, no external services.
# Build with cgo disabled so the result is a fully-static binary (ADR 0003).

BINARY := life-ledger
PKG    := ./cmd/life-ledger
export CGO_ENABLED = 0

.PHONY: run build test vet fmt clean hash-password

## run: start the server locally (one command, no external services).
run:
	go run $(PKG)

## hash-password: generate a bcrypt hash to paste into LIFELEDGER_PASSWORD_HASH.
hash-password:
	@go run ./cmd/hashpw

## build: compile the static binary into ./bin.
build:
	go build -o bin/$(BINARY) $(PKG)

## test: run the full test suite.
test:
	go test ./...

## vet: static analysis.
vet:
	go vet ./...

## fmt: format all Go sources.
fmt:
	go fmt ./...

## clean: remove build artifacts.
clean:
	go clean
	rm -rf bin
