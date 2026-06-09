# toktop developer tasks.
#
# CGO and the sqlite_fts5 build tag are mandatory: the SQLite driver is cgo-based
# and toktop refuses to run without FTS5. Every target carries both.

GO ?= go
TAGS := sqlite_fts5
export CGO_ENABLED = 1

# Stamp build metadata into main.{version,commit,date} via -ldflags -X, mirroring
# the release workflow. version/commit come from git; date is the build time.
# LDFLAGS is lazy (=) so only `build` pays for the git/date shell-outs, not
# vet/lint/etc. Override on demand, e.g. `make build VERSION=v1.2.3`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  = -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build vet vuln lint check fmt

build:
	$(GO) build -tags $(TAGS) -ldflags "$(LDFLAGS)" -o toktop ./cmd/toktop

vet:
	$(GO) vet -tags $(TAGS) ./...

fmt:
	$(GO) fmt ./...

# Supply-chain vulnerability scan. Run periodically and before a release.
# Install once: go install golang.org/x/vuln/cmd/govulncheck@latest
vuln:
	govulncheck -tags $(TAGS) ./...

# Static-analysis + idiom + modernization lint. staticcheck catches bugs, dead
# code, and simplifications; perfsprint catches fmt.Sprintf/Errorf that should be
# strconv/errors; modernize flags outdated patterns that newer Go features
# replace (manual loops -> slices.ContainsFunc, if-guards -> min/max,
# interface{} -> any, for-i -> range int, …); usestdlibvars flags string/number
# literals that have a stdlib constant. None of these are visible to `go vet`.
# --default=none scopes the gate to this set so it does not trip on the
# codebase's deliberate unchecked Close()/Fprint (errcheck) convention.
# Install once: brew install golangci-lint
#   (or: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)
lint:
	golangci-lint run --build-tags $(TAGS) --default=none \
		-E staticcheck -E perfsprint -E modernize -E usestdlibvars ./...

# Pre-release gate: vet, static-analysis/idiom lint, scan dependencies for known
# CVEs, then build.
check: vet lint vuln build
