# Common dev tasks. CI runs the same gates from .github/workflows/ci.yml.

GO       ?= go
BIN      ?= ./google-chat-mcp
VERSION  ?= dev
PKG       = github.com/mmedum/google-chat-mcp/v9
LDFLAGS   = -s -w -X $(PKG)/internal/version.Version=$(VERSION)
COVER_MIN ?= 80
# The range schema-ack measures on a local run; CI passes its own.
PR_BASE_REF ?= origin/main
PR_HEAD     ?= HEAD

.PHONY: all
all: check

.PHONY: build
build: ## Build the binary
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/google-chat-mcp

.PHONY: install
install: ## go install the binary
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags="$(LDFLAGS)" ./cmd/google-chat-mcp

# Rehearses the release: the six archives, built and thrown away. The
# goreleaser version is pinned here, never printed in a doc, so a
# rehearsal cannot run whatever goreleaser is on the PATH. The pins gate
# holds this and release.yml to the same version. The Windows version
# resource the before hook writes is removed after, pass or fail, so a
# later Windows build here does not carry the rehearsal's version.
.PHONY: release-rehearse
release-rehearse: ## Build the release archives locally, publishing nothing
	$(GO) run github.com/goreleaser/goreleaser/v2@v2.18.1 release --snapshot --clean --skip=sign,sbom,publish; \
	status=$$?; $(RM) cmd/*/rsrc_windows_*.syso; exit $$status

.PHONY: fmt
fmt: ## Fail if gofmt would change anything
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt issues:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## go vet, including the tagged suites so they keep compiling
	$(GO) vet ./...
	$(GO) vet -tags=evals ./...
	$(GO) vet -tags=live ./...

# The golangci-lint CI runs, at the version the pins gate holds to ci.yml,
# rather than whichever one is on the PATH.
.PHONY: lint
lint:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run

.PHONY: test
test: ## Unit tests with race detector and coverage
	$(GO) test -race -coverpkg=./cmd/...,./internal/... -coverprofile=cov.out -covermode=atomic ./...

# LEAKCHECK_HISTORY scans the commits a push would add, which the tree
# scan cannot see: an identifier committed and edited out later is still
# published. "all" widens it to every commit, for deciding whether the
# published past needs rewriting.
.PHONY: leaks-history
leaks-history: ## Nothing identifying a real account in the commits a push would add
	LEAKCHECK_HISTORY=1 $(GO) test ./internal/leakcheck/ -run TestTheHistoryIsClean -v

.PHONY: tidy
tidy: ## go.mod and go.sum are what `go mod tidy` would write
	$(GO) mod tidy -diff

# The release config is only ever exercised by a tag, and by then the
# mistake is public; this catches a config-level one, such as an unknown
# key or a build id nothing refers to.
.PHONY: goreleaser-check
goreleaser-check: ## The release config is valid
	$(GO) run github.com/goreleaser/goreleaser/v2@v2.18.1 check

.PHONY: cover
cover: test ## Enforce the coverage floor on core packages
	@$(GO) run ./scripts/gates coverage cov.out

.PHONY: vuln
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

.PHONY: licenses
licenses:
	$(GO) run github.com/google/go-licenses/v2@v2.0.1 check ./... --allowed_licenses=Apache-2.0,BSD-2-Clause,BSD-3-Clause,MIT,ISC

# These two run from source through the Go module proxy, not as images:
# Docker Hub rate-limits pulls from shared CI runners.
#
# gitleaks walks `git log --all` unless --log-opts names the commits
# (https://github.com/gitleaks/gitleaks/blob/v8.30.1/sources/git.go#L90-L94),
# and CI's full clone holds every pushed branch, so this scans HEAD's
# history alone. CI checks out the whole history for it.
.PHONY: gitleaks
gitleaks: ## No credential in HEAD's history
	$(GO) run github.com/zricethezav/gitleaks/v8@v8.30.1 git --log-opts=HEAD . --config .gitleaks.toml --redact --no-banner

.PHONY: actionlint
actionlint: ## Every workflow passes actionlint
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -color

.PHONY: pins
pins: ## Every third-party tool held to an exact version
	@$(GO) run ./scripts/gates pins

.PHONY: classes
classes: ## The tool error vocabulary, closed from both sides
	@$(GO) run ./scripts/gates classes

.PHONY: transcript
transcript: ## The drivers put nothing in their transcript unredacted
	@$(GO) run ./scripts/gates transcript

.PHONY: api-coverage
api-coverage: ## Every API method used on purpose or left out on purpose
	@$(GO) run ./scripts/gates api-coverage

.PHONY: api-fields
api-fields: ## Every published field modeled on purpose or left out on purpose
	@$(GO) run ./scripts/gates api-fields

.PHONY: docs
docs: ## Every decision, citation and named path resolves
	@$(GO) run ./scripts/gates docs

# Not part of check: it fetches Google's discovery documents, and a gate
# that fails when Google is slow is one people learn to rerun until it
# passes. Run it when you want to know whether the API has grown.
.PHONY: api-diff
api-diff: ## Refetch the API method list and report what changed (needs the network)
	$(GO) run ./scripts/gates api-diff

# This already runs under `cover`, as part of `go test ./...`. Naming it
# costs a fraction of a second and buys two things: it prints in the
# check output, and it can be run alone while working on what it guards.
# The shape is `live-surface`'s — a target that runs one test by name —
# rather than a `gates` subcommand, which would mean a Go program
# shelling out to `go test`.
.PHONY: leaks
leaks: ## Nothing identifying a real account, and nothing compiled, in the tree
	@$(GO) test ./internal/leakcheck -run 'TestTheRepositoryIsClean|TestNoBuildOutputInTheTree' -count=1

.PHONY: parity
parity: ## `make check` and CI run the same gates
	@$(GO) run ./scripts/gates parity

.PHONY: schemas
schemas: build ## Dump tool schemas
	$(BIN) --dump-schemas > schemas.json

.PHONY: schema-diff
schema-diff: build ## Diff tool schemas against the released baseline
	@$(GO) run ./scripts/gates schema-diff $(BIN)

.PHONY: schema-ack
schema-ack: ## Check every tool-surface change since the merge-base is named in a commit footer (a pull request's gate)
	@$(GO) run ./scripts/gates schema-ack $(PR_BASE_REF) $(PR_HEAD)

.PHONY: schema-baseline
schema-baseline: build ## Record the release being cut as the baseline (VERSION=vX.Y.Z, in the release commit)
	@$(GO) run ./scripts/gates schema-baseline $(BIN)

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
# if it only runs when someone remembers to run the live suite. The
# driver's own unit tests run here too, its cleanup's among them: they
# answer its calls with a fake, so everything but TestLive runs.
.PHONY: live-surface
live-surface: build ## Every tool and option is exercised by the live driver, and its cleanup holds
	@$(GO) test -tags=live ./internal/livecheck -count=1 -skip '^TestLive$$'

.PHONY: live
live: build ## Drive the shipped binary against a real account
	$(GO) test -tags=live ./internal/livecheck -v -count=1 -timeout 20m

.PHONY: check
check: fmt vet lint cover vuln licenses gitleaks actionlint pins classes transcript api-coverage api-fields docs leaks parity smoke schema-diff live-surface staleness tidy leaks-history goreleaser-check ## Everything CI runs

.PHONY: clean
clean:
	$(RM) $(BIN) cov.out schemas.json cmd/*/rsrc_windows_*.syso

# Lookups a maintainer runs by hand; nothing in a pipeline needs them.
.PHONY: config-vars scopes tool-names mcpb-manifest
config-vars: ## Print every GCM_ variable the server reads (manual)
	@$(GO) run ./scripts/gates config-vars
scopes: ## Print every OAuth scope the server can request (manual)
	@$(GO) run ./scripts/gates scopes
tool-names: schemas ## Print the tool names in the schema dump (manual)
	@$(GO) run ./scripts/gates tool-names schemas.json
mcpb-manifest: ## Print the bundle manifest with VERSION in it (manual)
	@$(GO) run ./scripts/gates mcpb-manifest $(VERSION)
