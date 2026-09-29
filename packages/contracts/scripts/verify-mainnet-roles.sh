#!/usr/bin/env bash
# =============================================================================
# Verify mainnet privileged-role custody (#1374)
#
# Read-only. Checks, for every protocol contract, that each privileged role is
# held by the admin multisig account contract and NOT by the deployer or the
# API operator key. Exits non-zero on any violation, so it can gate a launch
# checklist or run on a schedule.
#
# Usage:
#   export ADMIN_MULTISIG=C...      # multisig account contract (Admin/Upgrader/Treasurer)
#   export GUARDIAN_MULTISIG=C...   # optional; defaults to ADMIN_MULTISIG
#   export DEPLOYER=G...            # key that deployed the contracts
#   export OPERATOR=G...            # API operator key (STELLAR_OPERATOR_SECRET)
#   export SOURCE_ACCOUNT=<stellar-cli identity>   # any funded account; nothing is signed or sent
#   bash scripts/verify-mainnet-roles.sh scripts/deployed-mainnet.env
#
# The env file provides the contract IDs (same keys deploy-testnet.sh writes):
#   NESTER_ID VAULT_USDC_ID VAULT_XLM_ID TREASURY_ID YIELD_REGISTRY_ID
#   ALLOCATION_STRATEGY_ID [RECURRING_DEPOSIT_ID] [VAULT_FACTORY_ID]
# =============================================================================
set -euo pipefail

ENV_FILE="${1:?usage: verify-mainnet-roles.sh <deployed-mainnet.env>}"
# shellcheck disable=SC1090
source "$ENV_FILE"

NETWORK="${NETWORK:-mainnet}"
: "${ADMIN_MULTISIG:?ADMIN_MULTISIG is required}"
: "${DEPLOYER:?DEPLOYER is required}"
: "${OPERATOR:?OPERATOR is required}"
: "${SOURCE_ACCOUNT:?SOURCE_ACCOUNT is required}"
GUARDIAN_MULTISIG="${GUARDIAN_MULTISIG:-$ADMIN_MULTISIG}"

failures=0

has_role() { # contract account role -> prints true|false
  stellar contract invoke --network "$NETWORK" --source-account "$SOURCE_ACCOUNT" \
    --send=no --id "$1" -- has_role --account "$2" --role "$3" 2>/dev/null | tr -d '"[:space:]'
}

expect() { # label contract account role want
  local got
  got=$(has_role "$2" "$3" "$4" || echo "error")
  if [[ "$got" == "$5" ]]; then
    printf '  ok    %-22s %-10s %s=%s\n' "$1" "$4" "${3:0:8}…" "$got"
  else
    printf '  FAIL  %-22s %-10s %s expected %s, got %s\n' "$1" "$4" "${3:0:8}…" "$5" "$got"
    failures=$((failures + 1))
  fi
}

check() { # label contract roles...
  local label="$1" id="$2"; shift 2
  [[ -z "$id" ]] && return
  echo "$label ($id)"
  for role in "$@"; do
    local holder="$ADMIN_MULTISIG"
    [[ "$role" == "Guardian" ]] && holder="$GUARDIAN_MULTISIG"
    expect "$label" "$id" "$holder" "$role" true
    expect "$label" "$id" "$DEPLOYER" "$role" false
    expect "$label" "$id" "$OPERATOR" "$role" false
  done
}

check "nester"              "${NESTER_ID:-}"              Admin Upgrader
check "vault_usdc"          "${VAULT_USDC_ID:-}"          Admin Upgrader Guardian
check "vault_xlm"           "${VAULT_XLM_ID:-}"           Admin Upgrader Guardian
check "treasury"            "${TREASURY_ID:-}"            Admin Upgrader Treasurer
check "yield_registry"      "${YIELD_REGISTRY_ID:-}"      Admin Upgrader
check "allocation_strategy" "${ALLOCATION_STRATEGY_ID:-}" Admin Upgrader
check "recurring_deposit"   "${RECURRING_DEPOSIT_ID:-}"   Admin Upgrader
check "vault_factory"       "${VAULT_FACTORY_ID:-}"       Admin

if (( failures > 0 )); then
  echo "❌ $failures role custody violation(s)"
  exit 1
fi
echo "✅ all privileged roles held by the multisig"
