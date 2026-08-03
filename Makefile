SHELL := /bin/bash
COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env
GO_SERVICES := dispatch-service retry-service
TS_SERVICES := self-service-api subscription-service monitor-service demo-tools

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------
# environment
# ---------------------------------------------------------------------

.env:
	@cp .env.example .env && echo "created .env from .env.example"

## up: start infrastructure and services
.PHONY: up
up: .env
	$(COMPOSE) up -d --build
	@echo
	@echo "postgres    localhost:5432"
	@echo "kafka       localhost:9092"
	@echo "grafana     http://localhost:3000"
	@echo "prometheus  http://localhost:9090"

## down: stop everything, keep data
.PHONY: down
down:
	$(COMPOSE) down

## reset: stop everything and wipe volumes (fresh schema on next up)
.PHONY: reset
reset:
	$(COMPOSE) down -v

## ps: show container status
.PHONY: ps
ps:
	$(COMPOSE) ps

## logs: tail logs (make logs S=dispatch-service)
.PHONY: logs
logs:
	$(COMPOSE) logs -f $(S)

## psql: open a psql shell against the platform database
.PHONY: psql
psql:
	$(COMPOSE) exec postgres psql -U $${POSTGRES_USER:-notifications} -d $${POSTGRES_DB:-notifications}

## topics: list Kafka topics
.PHONY: topics
topics:
	$(COMPOSE) exec kafka kafka-topics --bootstrap-server localhost:9092 --list

# ---------------------------------------------------------------------
# demo
# ---------------------------------------------------------------------

## seed: load fixtures/notification_events.json as the initial platform state
.PHONY: seed
seed:
	cd services/demo-tools && pnpm run seed

## deliver-all: push every fixture event through the real delivery pipeline
.PHONY: deliver-all
deliver-all:
	cd services/demo-tools && pnpm run deliver-all

# ---------------------------------------------------------------------
# quality
# ---------------------------------------------------------------------

## install: install workspace dependencies
.PHONY: install
install:
	pnpm install

## build: compile every service
.PHONY: build
build:
	@for s in $(GO_SERVICES); do echo "== build $$s"; (cd services/$$s && go build ./...) || exit 1; done
	pnpm -r build

## test: unit tests for every service
.PHONY: test
test:
	@for s in $(GO_SERVICES); do echo "== test $$s"; (cd services/$$s && go test ./...) || exit 1; done
	pnpm -r test

## test-integration: tests that spin up real Postgres/Kafka via testcontainers
.PHONY: test-integration
test-integration:
	@for s in $(GO_SERVICES); do echo "== integration $$s"; (cd services/$$s && go test -tags=integration ./...) || exit 1; done

## lint: static analysis, including the architecture dependency rule
.PHONY: lint
lint:
	@for s in $(GO_SERVICES); do echo "== lint $$s"; (cd services/$$s && golangci-lint run ./...) || exit 1; done
	pnpm lint

## e2e: full end-to-end run against a live stack
.PHONY: e2e
e2e:
	./scripts/e2e.sh

## help: list targets
.PHONY: help
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /' | sort
