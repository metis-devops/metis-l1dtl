#!/bin/sh
# Copy and fill in the two required deployment values and the RPC endpoint.
set -eu
: "${L1_RPC:?Set L1_RPC}"
: "${ADDRESS_MANAGER:?Set ADDRESS_MANAGER}"
: "${CTC_DEPLOYMENT_BLOCK:?Set CTC_DEPLOYMENT_BLOCK}"
exec ./bin/metis-l1dtl \
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
