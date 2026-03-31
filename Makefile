backend:
	cd backend && python3 -m uvicorn app.main:app --reload --port 8042

frontend:
	cd frontend && npm run dev

dev:
	@echo "Run backend and frontend in separate terminals:"
	@echo "  make backend"
	@echo "  make frontend"

build-ui:
	cd frontend && npm run build

scan:
	cd backend && python3 -m app.cli load-workspace /Users/for_home/Developer/CoTitanMigration/Co_Titan
