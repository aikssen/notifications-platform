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
	$(COMPOSE) up --build
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

## story: tail only the delivery narration — what to project during the demo
.PHONY: story
story:
	$(COMPOSE) logs -f --tail=0 dispatch-service retry-service demo-tools

## quiet: restart the stack with only warnings and errors
.PHONY: quiet
quiet:
	LOG_LEVEL=warn $(COMPOSE) up -d
	@echo "log level is now warn — run 'make loud' to get the delivery narration back"

## loud: restore normal logging
.PHONY: loud
loud:
	LOG_LEVEL=info $(COMPOSE) up -d

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

# The demo scripts run on the host and talk to the published ports, so they
# need host-facing values rather than the in-network ones the containers use.
DEMO_ENV := DATABASE_URL="postgres://notifications:notifications@localhost:5432/notifications?sslmode=disable" \
            KAFKA_BROKERS=localhost:9092 \
            SUBSCRIPTIONS_BASE_URL=http://localhost:3001 \
            WEBHOOK_CONTROL_URL=http://localhost:3004 \
            FIXTURE_PATH=../../fixtures/notification_events.json

## subscribe-all: register a subscription for every client and event type in the fixture
.PHONY: subscribe-all
subscribe-all:
	cd services/demo-tools && $(DEMO_ENV) WEBHOOK_URL=$${WEBHOOK_URL:-http://demo-tools:3004/webhook} pnpm run subscribe-all

## deliver-all: push every fixture event through the real delivery pipeline
.PHONY: deliver-all
deliver-all:
	cd services/demo-tools && $(DEMO_ENV) pnpm run deliver-all

## seed: load the fixture as settled history instead (alternative to deliver-all)
.PHONY: seed
seed:
	cd services/demo-tools && $(DEMO_ENV) pnpm run seed

## reset-events: clear notification data, keeping subscriptions
.PHONY: reset-events
reset-events:
	$(COMPOSE) exec -T postgres psql -U $${POSTGRES_USER:-notifications} -d $${POSTGRES_DB:-notifications} \
	  -c "TRUNCATE notification_attempts, notification_events CASCADE;"
	@echo "notification events cleared"

## fail-next: make the demo webhook reject the next N deliveries (make fail-next N=20)
.PHONY: fail-next
fail-next:
	@curl -s -X POST localhost:3004/control -H 'content-type: application/json' \
	  -d '{"failNext": $(or $(N),3)}' && echo

## webhook-ok: put the demo webhook back to succeeding
.PHONY: webhook-ok
webhook-ok:
	@curl -s -X POST localhost:3004/control -H 'content-type: application/json' -d '{"reset":true}' && echo

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
