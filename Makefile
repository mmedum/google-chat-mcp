# Common dev tasks. CI runs the same gates from .github/workflows/ci.yml.

GO       ?= go
BIN      ?= ./google-chat-mcp
VERSION  ?= dev
PKG       = github.com/mmedum/google-chat-mcp/v2
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

.PHONY: pins
pins: ## Every third-party tool held to an exact version
	@$(GO) run ./scripts/gates pins

.PHONY: classes
classes: ## The tool error vocabulary, closed from both sides
	@$(GO) run ./scripts/gates classes

.PHONY: api-coverage
api-coverage: ## Every API method used on purpose or left out on purpose
	@$(GO) run ./scripts/gates api-coverage

# Not part of check: it fetches Google's discovery documents, and a gate
# that fails when Google is slow is one people learn to rerun until it
# passes. Run it when you want to know whether the API has grown.
.PHONY: api-diff
api-diff: ## Refetch the API method list and report what changed (needs the network)
	$(GO) run ./scripts/gates api-diff

# Both of these already run under `cover`, as part of `go test ./...`.
# Naming them costs a fraction of a second and buys two things: they
# print in the check output, and they can be run alone while working on
# what they guard. The shape is `live-surface`'s — a target that runs one
# test by name — rather than a `gates` subcommand, which would mean a Go
# program shelling out to `go test`.
.PHONY: leaks
leaks: ## Nothing identifying a real account, and nothing compiled, in the tree
	@$(GO) test ./internal/leakcheck -run 'TestTheRepositoryIsClean|TestNoBuildOutputInTheTree' -count=1

.PHONY: parity
parity: ## `make check` and CI run the same gates
	@$(GO) test ./scripts/gates -run TestMakeCheckAndCIRunTheSameGates -count=1

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
live-surface: build ## Every tool and option is exercised by the live driver, and every print is redacted
	@$(GO) test -tags=live ./internal/livecheck -count=1 \
		-run 'TestEveryToolIsExercisedOrExcused|TestEveryToolOptionIsExercisedOrExcused|TestEveryPrintGoesThroughTheRedactor' 

.PHONY: live
live: build ## Drive the shipped binary against a real account
	$(GO) test -tags=live ./internal/livecheck -v -count=1 -timeout 20m

.PHONY: check
check: fmt vet lint cover vuln licenses pins classes api-coverage leaks parity smoke schema-diff live-surface staleness ## Everything CI runs

.PHONY: clean
clean:
	$(RM) $(BIN) cov.out schemas.json
