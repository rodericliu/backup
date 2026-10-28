VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/rodericliu/backup/internal/version.Version=$(VERSION)
GOOS ?= linux
GOARCH ?= amd64

.PHONY: all build build-go web web-check test vet fmt fmt-check fix tidy clean

all: fmt-check vet test web-check build

# build embeds the web UI into backup-server; build-go skips the UI.
build: web build-go

build-go:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

web: web/node_modules
	cd web && npm run build

web-check: web/node_modules
	cd web && npm run typecheck

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	touch web/node_modules

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
	rm -rf bin web/node_modules
	find web/dist -mindepth 1 ! -name .gitkeep -delete
