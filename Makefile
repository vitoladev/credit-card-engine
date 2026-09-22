.PHONY: test synth floci-up floci-down local-bootstrap local-deploy local-destroy api-url loadtest

COMPOSE := docker compose -f .devcontainer/docker-compose.yml

test:
	npx turbo run test

synth:
	cdk synth

floci-up:
	@if echo "$${AWS_ENDPOINT_URL:-}" | grep -q '://floci'; then \
		echo "Floci já é o sidecar do Dev Container ($$AWS_ENDPOINT_URL)"; \
	else \
		$(COMPOSE) up -d floci; \
		echo "Floci em http://localhost:4566"; \
	fi

floci-down:
	@if echo "$${AWS_ENDPOINT_URL:-}" | grep -q '://floci'; then \
		echo "Feche o Dev Container para parar o compose (shutdownAction: stopCompose)"; \
	else \
		$(COMPOSE) stop floci; \
	fi

local-bootstrap:
	. scripts/floci.env && cdklocal bootstrap aws://000000000000/us-east-1

local-deploy:
	. scripts/floci.env && cdklocal deploy --require-approval never

local-destroy:
	. scripts/floci.env && cdklocal destroy --force

api-url:
	. scripts/floci.env && bash scripts/api-url.sh

loadtest:
	. scripts/floci.env && BASE_URL="$$(bash scripts/api-url.sh)" npx turbo run loadtest --filter=loadtest
