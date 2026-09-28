# xMustard is Go (api-go) + a Rust core (rust-core).
#
# PREFIX controls the install location (default /usr/local). `make install`
# installs the Rust core and the three Go binaries. The Go API resolves the core
# through XMUSTARD_CORE_BIN or PATH (no wrapper is installed).

PREFIX ?= /usr/local
BINDIR := $(PREFIX)/bin

.PHONY: build install relay backend backend-platform go-api frontend go-api-build rust-core-check \
	rust-core-scan migration-check dev build-ui scan check check-backend check-frontend
.PHONY: bench-test bench-gate bench-parity bench-retrieval
.PHONY: release release-sums

build:
	cd rust-core && cargo build --release --bin xmustard-core --bin xmustard-relay
	cd api-go && go build -o bin/xmustard-api ./cmd/xmustard-api
	cd api-go && go build -o bin/xmustard-mcp ./cmd/xmustard-mcp
	cd api-go && go build -o bin/xmustard-ops ./cmd/xmustard-ops

install: build
	install -d "$(BINDIR)"
	install -m 0755 rust-core/target/release/xmustard-core "$(BINDIR)/xmustard-core"
	install -m 0755 rust-core/target/release/xmustard-relay "$(BINDIR)/xmustard-relay"
	install -m 0755 api-go/bin/xmustard-api "$(BINDIR)/xmustard-api"
	install -m 0755 api-go/bin/xmustard-mcp "$(BINDIR)/xmustard-mcp"
	install -m 0755 api-go/bin/xmustard-ops "$(BINDIR)/xmustard-ops"

# The native stdio relay for MCP clients that can only launch a command: it speaks to
# the API's Streamable HTTP endpoint (/mcp), std-only Rust, about 2 MiB RSS.
relay:
	cd rust-core && cargo build --release --bin xmustard-relay

# Release archive for this host: `make release VERSION=v0.2.0`. It builds the Rust core
# and relay with cargo --locked and the Go binaries with -trimpath and CGO off (static, so
# the platform profile's PTY terminals, which need cgo, are unavailable in them), checks
# that the built binaries start, and writes $(DIST)/xmustard-<version>-<os>-<arch>.tar.gz
# (the five binaries and LICENSE) with its .sha256, then SHA256SUMS (release-sums). The
# tag workflow (.github/workflows/release.yml) runs it once per platform. RUSTFLAGS and
# CFLAGS (for the C in tree-sitter and SQLite) are replaced so the binaries hold no
# build-machine paths (the checkout becomes ., CARGO_HOME /cargo), and every archive entry
# gets the commit's time, a fixed mode and owner 0, so a rebuild of a commit with the same
# toolchains gives the same archive bytes.
DIST ?= dist
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
SOURCE_DATE := $(shell TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y%m%d%H%M.%S 2>/dev/null)
RELEASE_NAME := xmustard-$(VERSION)-$(shell uname -s | tr '[:upper:]' '[:lower:]')-$(shell uname -m)
RELEASE_DIR := $(abspath $(DIST))/$(RELEASE_NAME)
RUST_BINS := xmustard-core xmustard-relay
GO_BINS := xmustard-api xmustard-mcp xmustard-ops
RELEASE_FILES := $(sort LICENSE $(RUST_BINS) $(GO_BINS))

release:
	rm -rf "$(RELEASE_DIR)" "$(RELEASE_DIR).tar" "$(RELEASE_DIR).tar.gz" "$(RELEASE_DIR).tar.gz.sha256"
	mkdir -p "$(RELEASE_DIR)"
	cd rust-core && cargo_home=$${CARGO_HOME:-$$HOME/.cargo} && \
		RUSTFLAGS="--remap-path-prefix=$(CURDIR)=. --remap-path-prefix=$$cargo_home=/cargo" \
		CFLAGS="-ffile-prefix-map=$(CURDIR)=. -ffile-prefix-map=$$cargo_home=/cargo" \
		cargo build --release --locked $(RUST_BINS:%=--bin %)
	cp $(RUST_BINS:%=rust-core/target/release/%) LICENSE "$(RELEASE_DIR)/"
	cd api-go && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$(RELEASE_DIR)/" $(GO_BINS:%=./cmd/%)
	"$(RELEASE_DIR)/xmustard-core" 2>&1 | grep -q usage
	printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | "$(RELEASE_DIR)/xmustard-mcp" | grep -q '"remember"'
	cd "$(RELEASE_DIR)" && chmod 0755 . $(RUST_BINS) $(GO_BINS) && chmod 0644 LICENSE && \
		TZ=UTC0 touch -t $(or $(SOURCE_DATE),197001010000.00) . $(RELEASE_FILES)
	cd "$(DIST)" && COPYFILE_DISABLE=1 tar --no-recursion --no-xattrs --owner=0 --group=0 --numeric-owner \
		-cf "$(RELEASE_NAME).tar" "$(RELEASE_NAME)" $(RELEASE_FILES:%=$(RELEASE_NAME)/%)
	cd "$(DIST)" && gzip -n -9 "$(RELEASE_NAME).tar"
	cd "$(DIST)" && shasum -a 256 "$(RELEASE_NAME).tar.gz" > "$(RELEASE_NAME).tar.gz.sha256"
	$(MAKE) --no-print-directory release-sums VERSION="$(VERSION)" DIST="$(DIST)"

# $(DIST)/SHA256SUMS for every platform archive of $(VERSION) in $(DIST), each checked
# against its .sha256 first. The tag workflow runs it once both platforms' archives are in.
release-sums:
	cd "$(DIST)" && shasum -a 256 -c xmustard-$(VERSION)-*.tar.gz.sha256 && \
		cat xmustard-$(VERSION)-*.tar.gz.sha256 | LC_ALL=C sort -k 2 > SHA256SUMS
	cat "$(DIST)/SHA256SUMS"

# The API defaults to the core profile (the nine tools, memory, evidence, auth).
# The UI calls platform routes, so the UI targets start it with XMUSTARD_PROFILE=platform.
backend:
	cd api-go && XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api

backend-platform:
	cd api-go && XMUSTARD_PROFILE=platform XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api

go-api: backend

# The UI dev server alone; it proxies /api to :8042, which must run the platform
# profile (make backend-platform, or make dev for both).
frontend:
	cd frontend && npm run dev

go-api-build:
	cd api-go && go build ./cmd/xmustard-api

rust-core-check:
	cd rust-core && cargo check

rust-core-scan:
	cd rust-core && cargo run --quiet --bin xmustard-core -- scan-signals $(ROOT)

migration-check:
	cd api-go && go build ./cmd/xmustard-api
	cd rust-core && cargo check

# UI development: the API in the platform profile plus the UI dev server; Ctrl-C
# stops both.
dev:
	@trap 'kill $$(jobs -p) 2>/dev/null' INT TERM EXIT; \
		(cd api-go && XMUSTARD_PROFILE=platform XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api) & \
		(cd frontend && npm run dev) & \
		wait

build-ui:
	cd frontend && npm run build

scan:
	@test -n "$(ROOT)" || (echo "usage: make scan ROOT=/absolute/path/to/repo" >&2; exit 2)
	@scan_root=$$(cd "$(ROOT)" && pwd) && \
		cd api-go && go run ./cmd/xmustard-ops workspace load --root-path "$$scan_root"

check: check-backend check-frontend

check-backend:
	cd api-go && go test ./...
	cd api-go && go build ./...
	cd rust-core && cargo test
	cd rust-core && cargo clippy

check-frontend:
	cd frontend && npm run lint
	cd frontend && npm run build

# Budget gate v2 and the retrieval gate (scripts/bench). BENCH_OUT holds report.json and
# report.md per gate. WORKSTREAM + BASELINE add the ledger delta check (see budget_ledger.json).
BENCH_OUT ?= bench-out
BENCH_ARGS ?=

bench-test:
	python3 -m unittest discover -v -s scripts/bench -p 'test_*.py'

bench-gate:
	scripts/bench/rss_v2.sh run --suite ci --out $(BENCH_OUT)/gate $(BENCH_ARGS) \
		$(if $(WORKSTREAM),--workstream $(WORKSTREAM) --baseline $(BASELINE))

bench-parity:
	scripts/bench/rss_v2.sh run --suite parity --out $(BENCH_OUT)/parity $(BENCH_ARGS)

bench-retrieval:
	mkdir -p $(BENCH_OUT)
	scripts/bench/retrieval-gate.sh --report $(BENCH_OUT)/retrieval-gate.json
