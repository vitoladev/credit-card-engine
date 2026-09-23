#!/usr/bin/env bash
set -euo pipefail
: "${AWS_ENDPOINT_URL:=http://localhost:4566}"
: "${AWS_DEFAULT_REGION:=us-east-1}"
: "${AWS_ACCESS_KEY_ID:=test}"
: "${AWS_SECRET_ACCESS_KEY:=test}"

# The stack's own API, not the first one Floci lists: a failed update or
# another deploy can leave other APIs behind.
api_id="$(aws cloudformation describe-stack-resources --endpoint-url "$AWS_ENDPOINT_URL" \
  --stack-name "${STACK_NAME:-CreditCardEngine}" \
  --query "StackResources[?ResourceType=='AWS::ApiGatewayV2::Api'].PhysicalResourceId | [0]" \
  --output text)"
if [ -z "$api_id" ] || [ "$api_id" = "None" ]; then
  echo "api-url: stack ${STACK_NAME:-CreditCardEngine} has no API; run make local-deploy" >&2
  exit 1
fi
echo "${AWS_ENDPOINT_URL}/_aws/execute-api/${api_id}/\$default"
