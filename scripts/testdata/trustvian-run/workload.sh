#!/bin/sh
# The model-free workload behind the trustvian-run action's end-to-end
# workflow: processor/cmd/agent-producer, which emits crm_lookup,
# knowledge_search and export_customer as GenAI tool spans, in that order.
# SPAN_COUNT=2 never reaches export_customer; SPAN_COUNT=3 always does — so a
# candidate's verdict is fixed by its scenario, with no model involved.
#
# The workflow builds agent-producer to $RUNNER_TEMP/agent-producer before the
# action runs. `trustvian dev` sets TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT.
set -eu
OTLP_ENDPOINT="$TRUSTVIAN_DEV_OTLP_GRPC_ENDPOINT" \
MODE=semantic \
SERVICE_NAME="$OTEL_SERVICE_NAME" \
ENVIRONMENT=local \
exec "$RUNNER_TEMP/agent-producer"
