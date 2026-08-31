.PHONY: dev deps-down

dev:
	./scripts/dev.sh

deps-down:
	docker compose -f docker-compose.local.yml down
