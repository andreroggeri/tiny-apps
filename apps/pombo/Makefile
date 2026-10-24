BINARY := bin/pombo
PKG    := ./cmd/pombo
IMAGE  ?= pombo:dev

.PHONY: build test fmt vet lint run image clean

build:
	go build -o $(BINARY) $(PKG)

test:
	go test -race ./...

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

# CI-safe: fails if anything is unformatted, instead of rewriting it.
lint:
	@out=$$(gofmt -l cmd internal); \
	if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi
	go vet ./...

run:
	POMBO_DATA_DIR=./data go run $(PKG)

image:
	docker build -t $(IMAGE) .

clean:
	rm -rf bin
