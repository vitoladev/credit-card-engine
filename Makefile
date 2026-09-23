.PHONY: requests test synth floci-up floci-down floci-reap local-bootstrap local-deploy local-redeploy local-destroy api-url loadtest

COMPOSE := docker compose -f .devcontainer/docker-compose.yml

test:
	bash scripts/with-floci-reap.sh npx turbo run test

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
