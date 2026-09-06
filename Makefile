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
vet: ## go vet, including the tagged suites so they keep compiling
	$(GO) vet ./...
	$(GO) vet -tags=evals ./...
	$(GO) vet -tags=live ./...

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
	@$(GO) run ./scripts/gates coverage cov.out

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
	@$(GO) run ./scripts/gates schema-diff $(BIN)

.PHONY: smoke
smoke: build ## Drive the binary over stdio
	@$(GO) run ./scripts/gates smoke $(BIN)

.PHONY: staleness
staleness: build ## Docs must match the code
	@$(GO) run ./scripts/gates staleness $(BIN)

# Not part of check, and never part of CI: the evals need a login, the
# `claude` CLI, and real API spend, and they write to a real Workspace.
# Each task makes its own space and deletes it again unless it failed.
.PHONY: evals
evals: build ## Score a model driving these tools against a real account
	$(GO) test -tags=evals ./internal/evals -v -timeout 40m

# The live driver itself needs a login and writes to a real account, so
# it is never in check. Its surface gate is: it asks the built binary
# what it registers, which needs no credentials, and fails on a tool that
# no step exercises and no reason excuses. That is what keeps a tool
# added tomorrow from being silently unexercised, and it is worth nothing
# if it only runs when someone remembers to run the live suite.
.PHONY: live-surface
live-surface: build ## Every tool is exercised by the live driver or excused with a reason
	@$(GO) test -tags=live ./internal/livecheck -run TestEveryToolIsExercisedOrExcused -count=1

.PHONY: live
live: build ## Drive the shipped binary against a real account
	$(GO) test -tags=live ./internal/livecheck -v -count=1 -timeout 20m

.PHONY: check
check: fmt vet lint cover vuln licenses smoke schema-diff live-surface staleness ## Everything CI runs

.PHONY: clean
clean:
	$(RM) $(BIN) cov.out schemas.json
