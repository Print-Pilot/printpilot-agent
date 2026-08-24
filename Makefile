BINARY   := printpilot-agent
PKG      := github.com/Print-Pilot/printpilot-agent
DIST     := dist

GO       ?= go
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -ldflags "-X main.version=$(VERSION)"

.PHONY: all build build-amd64 build-386 build-arm build-arm64 release checksums test vet fmt clean

all: build

build:
	$(GO) build -o $(DIST)/$(BINARY) $(LDFLAGS) .

build-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -o $(DIST)/$(BINARY)-linux-amd64 $(LDFLAGS) .

build-386:
	GOOS=linux GOARCH=386 $(GO) build -o $(DIST)/$(BINARY)-linux-386 $(LDFLAGS) .

build-arm:
	GOOS=linux GOARCH=arm GOARM=7 $(GO) build -o $(DIST)/$(BINARY)-linux-arm $(LDFLAGS) .

build-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -o $(DIST)/$(BINARY)-linux-arm64 $(LDFLAGS) .

# Compila los cuatro binarios con la versión y los deja en dist/. El nombre
# incluye la arquitectura para facilitar la subida a GitHub Releases.
release: build-amd64 build-386 build-arm build-arm64
	@echo "Binarios listos en $(DIST)/ con versión $(VERSION)"

# Genera un .sha256 por binario (los verifica el instalador deploy/install.sh).
checksums:
	@for f in $(DIST)/$(BINARY)-linux-*; do \
		sha256sum "$$f" | awk '{print $$1}' > "$$f.sha256"; \
		echo "checksum: $$f.sha256"; \
	done

run:
	$(GO) run . $(ARGS)

run-echo:
	$(GO) run ./cmd/echoserver $(ARGS)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(DIST)