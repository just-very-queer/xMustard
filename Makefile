# The backend is now Go (api-go) + Rust core (rust-core). The legacy Python
# FastAPI backend was retired to archive/ once api-go reached route parity
# (120 routes >= 115) and a full passing test suite. See
# docs/PYTHON_TO_RUST_MIGRATION.md.

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
