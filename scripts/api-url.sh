#!/usr/bin/env bash
set -euo pipefail
: "${AWS_ENDPOINT_URL:=http://localhost:4566}"
: "${AWS_DEFAULT_REGION:=us-east-1}"
: "${AWS_ACCESS_KEY_ID:=test}"
: "${AWS_SECRET_ACCESS_KEY:=test}"

api_id="$(aws apigatewayv2 get-apis --endpoint-url "$AWS_ENDPOINT_URL" \
  --query 'Items[0].ApiId' --output text)"
echo "${AWS_ENDPOINT_URL}/_aws/execute-api/${api_id}/\$default"
