# Common dev tasks. CI runs the same gates from .github/workflows/ci.yml.

GO       ?= go
BIN      ?= ./google-chat-mcp
VERSION  ?= dev
PKG       = github.com/mmedum/google-chat-mcp
LDFLAGS   = -s -w -X $(PKG)/internal/version.Version=$(VERSION)
COVER_MIN ?= 80
GOBIN    := $(shell $(GO) env GOPATH)/bin
# Prefer tools installed with the current Go (go install ...@latest) over distro packages.
export PATH := $(GOBIN):$(PATH)

.PHONY: all
all: check

.PHONY: build
build: ## Build the binary
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/google-chat-mcp

.PHONY: install
install: ## go install the binary
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags="$(LDFLAGS)" ./cmd/google-chat-mcp

.PHONY: fmt
fmt: ## Fail if gofmt would change anything
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt issues:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## go vet, including the eval suite so it keeps compiling
	$(GO) vet ./...
	$(GO) vet -tags=evals ./...

.PHONY: lint
lint:
	golangci-lint run

.PHONY: test
test: ## Unit tests with race detector and coverage
# LEAKCHECK_HISTORY scans the commits a push would add, which the tree
# scan cannot see: an identifier committed and edited out later is still
# published. "all" widens it to every commit, for deciding whether the
# published past needs rewriting.
	LEAKCHECK_HISTORY=1 $(GO) test -race -coverpkg=./cmd/...,./internal/... -coverprofile=cov.out -covermode=atomic ./...

.PHONY: cover
cover: test ## Enforce the coverage floor on core packages
	@bash scripts/coverage-check.sh cov.out $(COVER_MIN)

.PHONY: vuln
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

.PHONY: licenses
licenses:
	go run github.com/google/go-licenses/v2@v2.0.1 check ./... --allowed_licenses=Apache-2.0,BSD-2-Clause,BSD-3-Clause,MIT,ISC

.PHONY: schemas
schemas: build ## Dump tool schemas
	$(BIN) --dump-schemas > schemas.json

.PHONY: schema-diff
schema-diff: build ## Diff tool schemas against the last tag
	@bash scripts/schema-diff.sh $(BIN)

.PHONY: smoke
smoke: build ## Drive the binary over stdio
	@bash scripts/stdio-smoke.sh $(BIN)

.PHONY: staleness
staleness: build ## Docs must match the code
	@bash scripts/staleness-check.sh $(BIN)

# Not part of check, and never part of CI: the evals need a login, the
# `claude` CLI, and real API spend, and they write to a real Workspace.
# Each task makes its own space and deletes it again unless it failed.
.PHONY: evals
evals: build ## Score a model driving these tools against a real account
	$(GO) test -tags=evals ./internal/evals -v -timeout 40m

.PHONY: check
check: fmt vet lint cover vuln licenses smoke schema-diff staleness ## Everything CI runs

.PHONY: clean
clean:
	$(RM) $(BIN) cov.out schemas.json
