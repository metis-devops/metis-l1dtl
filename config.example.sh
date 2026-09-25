#!/bin/sh
# Copy and fill in the two required deployment values and the RPC endpoint.
# Probe separately: ./bin/metis-l1dtl healthcheck
# Defaults: --url=http://127.0.0.1:7878/healthz --timeout=3s
# Use /readyz for readiness; update --url if changing --listen below.
set -eu
: "${L1_RPC:?Set L1_RPC}"
: "${ADDRESS_MANAGER:?Set ADDRESS_MANAGER}"
: "${CTC_DEPLOYMENT_BLOCK:?Set CTC_DEPLOYMENT_BLOCK}"
# Optional Blob mode requires all five BLOB_* values together.
# The start is independent of CTC_DEPLOYMENT_BLOCK; retention is seven days
# relative to the confirmed L1 head, with no transaction-count limit.
set --
if [ "${BLOB_BEACON+x}${BLOB_INBOX+x}${BLOB_START+x}${BLOB_BATCH_SENDER+x}${BLOB_SENDER+x}" != "" ]; then
  : "${BLOB_BEACON:?Set BLOB_BEACON}"
  : "${BLOB_INBOX:?Set BLOB_INBOX}"
  : "${BLOB_START:?Set BLOB_START}"
  : "${BLOB_BATCH_SENDER:?Set BLOB_BATCH_SENDER}"
  : "${BLOB_SENDER:?Set BLOB_SENDER}"
  set -- --l1-beacon="$BLOB_BEACON" --batch-inbox-address="$BLOB_INBOX" \
    --batch-inbox-l1-height="$BLOB_START" --batch-inbox-sender="$BLOB_BATCH_SENDER" \
    --batch-inbox-blob-sender="$BLOB_SENDER"
fi
exec ./bin/metis-l1dtl "$@" \
  --l1-rpc="$L1_RPC" \
  --l1-chain-id=1 \
  --l2-chain-id=1088 \
  --address-manager="$ADDRESS_MANAGER" \
  --l1-start-height="$CTC_DEPLOYMENT_BLOCK" \
  --confirmations=35 \
  --poll-interval=5s \
  --batch-size=2000 \
  --db=data \
  --listen=0.0.0.0:7878
