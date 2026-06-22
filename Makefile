# Aizorix monorepo task runner.
# Usage: make <target> [SERVICE=auth]

SHELL := /bin/bash
SERVICE ?= auth
MIGRATE_DB ?= $(DATABASE_URL)
GO ?= go

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## ── Local environment ───────────────────────────────────────────────
.PHONY: dev-up
dev-up: ## Start backing services (postgres, redis, redpanda, minio, es)
	docker compose up -d
	@echo "Waiting for postgres..." && sleep 4

.PHONY: dev-down
dev-down: ## Stop backing services
	docker compose down

.PHONY: dev-reset
dev-reset: ## Wipe local data volumes and restart
	docker compose down -v && rm -rf .data && $(MAKE) dev-up

## ── Database ────────────────────────────────────────────────────────
.PHONY: migrate-up
migrate-up: ## Apply all up migrations
	migrate -path db/migrations -database "$(MIGRATE_DB)" up

.PHONY: migrate-down
migrate-down: ## Roll back one migration
	migrate -path db/migrations -database "$(MIGRATE_DB)" down 1

.PHONY: migrate-new
migrate-new: ## Create a migration: make migrate-new NAME=add_x
	migrate create -ext sql -dir db/migrations -seq $(NAME)

## ── Codegen ─────────────────────────────────────────────────────────
.PHONY: generate
generate: proto sqlc ## Run all code generation

.PHONY: proto
proto: ## Generate Go from protobuf via buf
	cd api && buf generate

.PHONY: sqlc
sqlc: ## Generate type-safe DB access from SQL
	sqlc generate

## ── Build / run / test ──────────────────────────────────────────────
.PHONY: run
run: ## Run a service: make run SERVICE=auth
	cd services/$(SERVICE) && $(GO) run ./cmd/server

.PHONY: build
build: ## Build all service binaries into ./bin
	@for d in services/*/ ; do \
		name=$$(basename $$d); \
		if [ -d "$$d/cmd/server" ]; then \
			echo "building $$name"; \
			(cd $$d && $(GO) build -o ../../bin/$$name ./cmd/server); \
		fi; \
	done

.PHONY: test
test: ## Run all Go tests with race detector
	$(GO) test -race -count=1 ./...

.PHONY: lint
lint: ## Run golangci-lint across all modules
	golangci-lint run ./...

.PHONY: tidy
tidy: ## go mod tidy across the workspace
	@for d in services/*/ services/pkg ; do \
		if [ -f "$$d/go.mod" ]; then (cd $$d && $(GO) mod tidy); fi; \
	done

## ── Containers ──────────────────────────────────────────────────────
.PHONY: docker-build
docker-build: ## Build a service image: make docker-build SERVICE=auth
	docker build -f services/$(SERVICE)/Dockerfile -t aizorix/$(SERVICE):dev .

## ── Demo data & full-stack local run ────────────────────────────────
.PHONY: seed
seed: ## Insert demo data (users, projects, contracts, escrow) into DATABASE_URL
	cd services/tools && GOWORK=off DATABASE_URL="$(DATABASE_URL)" $(GO) run ./cmd/seed

.PHONY: services-up
services-up: ## Build & start the app-tier services (gateway on :8080) against running infra
	docker compose -f deploy/docker-compose.services.yml up --build -d

.PHONY: services-down
services-down: ## Stop the app-tier services
	docker compose -f deploy/docker-compose.services.yml down

.PHONY: smoke
smoke: ## Run the gateway smoke test (health, register, login, /v1/auth/me)
	bash scripts/smoke.sh

.PHONY: demo
demo: ## Bring the WHOLE platform up live on isolated ports (Windows/PowerShell)
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo.ps1

.PHONY: demo-down
demo-down: ## Tear down the live demo (services + infra)
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-down.ps1

.PHONY: test-integration
test-integration: ## Run integration tests (needs Docker; uses testcontainers)
	@for d in services/auth services/escrow services/contract ; do \
		echo "== $$d (integration) =="; (cd $$d && go test -tags=integration ./...); \
	done
