.PHONY: requests test coverage synth floci-up floci-down floci-reap local-bootstrap local-deploy local-redeploy local-destroy api-url loadtest

COMPOSE := docker compose -f .devcontainer/docker-compose.yml

test:
	bash scripts/with-floci-reap.sh npx turbo run test

# One coverage number for the engine and the CDK stack.
coverage:
	bash scripts/with-floci-reap.sh go test ./apps/engine/... ./packages/infra-iac/... \
		-count=1 -covermode=atomic \
		-coverpkg=engine/...,infra-iac/... \
		-coverprofile=coverage.out
	@go tool cover -func=coverage.out | tail -1

synth:
	cdk synth

floci-up:
	@if echo "$${AWS_ENDPOINT_URL:-}" | grep -q '://floci'; then \
		echo "Floci is already the Dev Container sidecar ($$AWS_ENDPOINT_URL)"; \
	else \
		$(COMPOSE) up -d floci; \
		echo "Floci at http://localhost:4566"; \
	fi

floci-down:
	@if echo "$${AWS_ENDPOINT_URL:-}" | grep -q '://floci'; then \
		echo "Stop the Dev Container to stop Compose (shutdownAction: stopCompose)"; \
	else \
		$(COMPOSE) stop floci; \
	fi

floci-reap:
	bash scripts/floci-reap.sh

local-bootstrap:
	. scripts/floci.env && cdklocal bootstrap aws://000000000000/us-east-1

local-deploy:
	. scripts/floci.env && cdklocal deploy --require-approval never

# Floci cannot update the relay's stream event source mapping in place, so a
# second deploy fails. Recreate the stack instead (the table starts empty).
local-redeploy: local-destroy local-deploy

local-destroy:
	. scripts/floci.env && cdklocal destroy --force; status=$$?; bash scripts/floci-reap.sh; exit $$status

api-url:
	. scripts/floci.env && bash scripts/api-url.sh

loadtest:
	. scripts/floci.env && BASE_URL="$$(bash scripts/api-url.sh)" bash scripts/with-floci-reap.sh npx turbo run loadtest --filter=loadtest

# Runs docs/requests (OpenCollection) with the Bruno CLI against the local stack.
requests:
	. scripts/floci.env && cd docs/requests && npx --yes @usebruno/cli@4.2.0 run -r --env floci --env-var "baseUrl=$$(cd ../.. && bash scripts/api-url.sh)"
