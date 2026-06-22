#!/usr/bin/env bash
# Smoke test for the Aizorix gateway.
#
# Steps (PASS/FAIL printed per step):
#   1. GET  /healthz                 (retried — the stack may still be warming up)
#   2. POST /v1/auth/register        (creates a throwaway freelancer)
#   3. POST /v1/auth/login           (obtains an access token)
#   4. GET  /v1/auth/me              (authenticated call; gateway injects identity)
#
# Usage:
#   scripts/smoke.sh                         # defaults to http://localhost:8080
#   GATEWAY_URL=http://host:8080 scripts/smoke.sh
#   scripts/smoke.sh http://host:8080
set -u

GATEWAY_URL="${GATEWAY_URL:-${1:-http://localhost:8080}}"
GATEWAY_URL="${GATEWAY_URL%/}"

PASS=0
FAIL=0
GREEN=$'\033[32m'; RED=$'\033[31m'; DIM=$'\033[2m'; RST=$'\033[0m'

pass() { echo "${GREEN}PASS${RST} $1"; PASS=$((PASS+1)); }
fail() { echo "${RED}FAIL${RST} $1"; FAIL=$((FAIL+1)); }

# json_field <json> <key> — extract a top-level string value without jq.
json_field() {
  printf '%s' "$1" | sed -n 's/.*"'"$2"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1
}

# Unique email so re-runs don't hit EMAIL_TAKEN.
EMAIL="smoke+$(date +%s)@aizorix.dev"
PASSWORD="SmokeTest123!"   # >= 12 chars to satisfy the auth service

echo "${DIM}Gateway: ${GATEWAY_URL}${RST}"
echo

# ── Step 1: health (with retries) ─────────────────────────────────────────────
HEALTH_OK=0
for i in $(seq 1 10); do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${GATEWAY_URL}/healthz" || true)
  if [ "$code" = "200" ]; then HEALTH_OK=1; break; fi
  echo "${DIM}  health attempt ${i}/10 -> ${code:-no-response}; retrying...${RST}"
  sleep 2
done
if [ "$HEALTH_OK" = "1" ]; then pass "GET /healthz -> 200"; else fail "GET /healthz never returned 200"; fi

# ── Step 2: register ──────────────────────────────────────────────────────────
REG_BODY=$(cat <<JSON
{"email":"${EMAIL}","password":"${PASSWORD}","account_type":"freelancer","residency_country":"US","locale":"en-US","accepted_terms":true,"accepted_monitoring_disclosure":true}
JSON
)
REG_RESP=$(curl -s -w $'\n%{http_code}' --max-time 10 -X POST \
  -H 'Content-Type: application/json' -d "${REG_BODY}" "${GATEWAY_URL}/v1/auth/register" || true)
REG_CODE=$(printf '%s' "$REG_RESP" | tail -n1)
REG_JSON=$(printf '%s' "$REG_RESP" | sed '$d')
if [ "$REG_CODE" = "201" ] || [ "$REG_CODE" = "200" ]; then
  pass "POST /v1/auth/register -> ${REG_CODE} (${EMAIL})"
else
  fail "POST /v1/auth/register -> ${REG_CODE}: ${REG_JSON}"
fi

# ── Step 3: login ─────────────────────────────────────────────────────────────
LOGIN_BODY=$(printf '{"email":"%s","password":"%s"}' "${EMAIL}" "${PASSWORD}")
LOGIN_RESP=$(curl -s -w $'\n%{http_code}' --max-time 10 -X POST \
  -H 'Content-Type: application/json' -d "${LOGIN_BODY}" "${GATEWAY_URL}/v1/auth/login" || true)
LOGIN_CODE=$(printf '%s' "$LOGIN_RESP" | tail -n1)
LOGIN_JSON=$(printf '%s' "$LOGIN_RESP" | sed '$d')
ACCESS_TOKEN=$(json_field "$LOGIN_JSON" "access_token")
if [ "$LOGIN_CODE" = "200" ] && [ -n "$ACCESS_TOKEN" ]; then
  pass "POST /v1/auth/login -> 200 (got access token)"
else
  fail "POST /v1/auth/login -> ${LOGIN_CODE}: ${LOGIN_JSON}"
fi

# ── Step 4: authenticated /me ─────────────────────────────────────────────────
if [ -n "$ACCESS_TOKEN" ]; then
  ME_RESP=$(curl -s -w $'\n%{http_code}' --max-time 10 \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" "${GATEWAY_URL}/v1/auth/me" || true)
  ME_CODE=$(printf '%s' "$ME_RESP" | tail -n1)
  ME_JSON=$(printf '%s' "$ME_RESP" | sed '$d')
  if [ "$ME_CODE" = "200" ]; then
    pass "GET /v1/auth/me -> 200 ($(json_field "$ME_JSON" "email"))"
  else
    fail "GET /v1/auth/me -> ${ME_CODE}: ${ME_JSON}"
  fi
else
  fail "GET /v1/auth/me skipped (no access token)"
fi

echo
echo "${DIM}────────────────────────────────────${RST}"
echo "Result: ${GREEN}${PASS} passed${RST}, ${RED}${FAIL} failed${RST}"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
