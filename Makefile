# xMustard is Go (api-go) + a Rust core (rust-core).
#
# PREFIX controls the install location (default /usr/local). `make install`
# installs the rust core and the three Go binaries, plus a wrapper that points
# the API/MCP at the installed core via XMUSTARD_CORE_BIN.

PREFIX ?= /usr/local
BINDIR := $(PREFIX)/bin

build:
	cd rust-core && cargo build --release --bin xmustard-core
	cd api-go && go build -o bin/xmustard-api ./cmd/xmustard-api
	cd api-go && go build -o bin/xmustard-mcp ./cmd/xmustard-mcp
	cd api-go && go build -o bin/xmustard-ops ./cmd/xmustard-ops

install: build
	install -d "$(BINDIR)"
	install -m 0755 rust-core/target/release/xmustard-core "$(BINDIR)/xmustard-core"
	install -m 0755 api-go/bin/xmustard-api "$(BINDIR)/xmustard-api"
	install -m 0755 api-go/bin/xmustard-mcp "$(BINDIR)/xmustard-mcp"
	install -m 0755 api-go/bin/xmustard-ops "$(BINDIR)/xmustard-ops"

backend:
	cd api-go && XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api

go-api: backend

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

dev:
	@echo "Run backend and frontend in separate terminals:"
	@echo "  make backend   # Go API on :8042 (calls the Rust core)"
	@echo "  make frontend"

build-ui:
	cd frontend && npm run build

scan:
	cd api-go && go run ./cmd/xmustard-ops load-workspace $(ROOT)
