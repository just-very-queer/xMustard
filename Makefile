backend:
	cd backend && python3 -m uvicorn app.main:app --reload --port 8042

frontend:
	cd frontend && npm run dev

go-api:
	cd api-go && go run ./cmd/xmustard-api

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
	@echo "  make backend"
	@echo "  make frontend"

build-ui:
	cd frontend && npm run build

scan:
	cd backend && python3 -m app.cli load-workspace /Users/for_home/Developer/CoTitanMigration/Co_Titan
