VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/rodericliu/backup/internal/version.Version=$(VERSION)
GOOS ?= linux
GOARCH ?= amd64

.PHONY: all build test vet fmt fmt-check fix tidy clean

all: fmt-check vet test build

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fix:
	go fix ./...

tidy:
	go mod tidy

clean:
	rm -rf bin
