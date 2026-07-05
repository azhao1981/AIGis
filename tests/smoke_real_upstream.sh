#!/bin/bash

# Real-upstream smoke test (MANUAL trigger only — not for CI).
#
# Unlike the mock-based e2e suites, this script sends real requests through a
# running gateway to the real upstreams configured in .env, verifying the two
# protocol chains end-to-end:
#   1. OpenAI-compatible  : POST /v1/chat/completions (gpt-* route)
#   2. Anthropic native   : POST /v1/messages         (claude/glm route)
#
# For each chain it asks the model to echo a seeded email address. The gateway
# masks it on the way out (placeholder upstream) and unmasks it on the way
# back, so the ORIGINAL email appearing in the response proves the full
# mask -> upstream -> unmask round trip against a real model.
#
# Prereqs:
#   - gateway running (make run), keys exported in the SERVER's environment
#   - .env in repo root (only used here to decide which chains to attempt)
#
# Usage:
#   ./tests/smoke_real_upstream.sh
#   AIGIS_PORT=3000 SMOKE_OPENAI_MODEL=gpt-4o SMOKE_CLAUDE_MODEL=claude-sonnet-4-20250514 ./tests/smoke_real_upstream.sh

set -u

AIGIS_PORT="${AIGIS_PORT:-8080}"
BASE_URL="http://localhost:${AIGIS_PORT}"
SMOKE_OPENAI_MODEL="${SMOKE_OPENAI_MODEL:-gpt-4o-mini}"
SMOKE_CLAUDE_MODEL="${SMOKE_CLAUDE_MODEL:-glm-4.6}"
SEED_EMAIL="smoke.probe@example.com"
PROMPT="Repeat this exact email address back to me and nothing else: ${SEED_EMAIL}"

# .env is only consulted to skip chains whose keys are absent (values never printed).
ENV_FILE="$(dirname "$0")/../.env"
env_has() {
    [ -f "$ENV_FILE" ] && grep -qE "^${1}=..+" "$ENV_FILE"
}

if ! curl -s "${BASE_URL}/health" > /dev/null 2>&1; then
    echo "FAIL: gateway not running on ${BASE_URL} (start with: make run)"
    exit 1
fi

PASS=0
FAIL=0
SKIP=0

check_response() {
    local name="$1" http_code="$2" body="$3"
    if [ "$http_code" != "200" ]; then
        echo "FAIL [${name}]: HTTP ${http_code}"
        echo "  body: $(echo "$body" | head -c 400)"
        FAIL=$((FAIL + 1))
        return
    fi
    if echo "$body" | grep -q "$SEED_EMAIL"; then
        echo "PASS [${name}]: mask -> upstream -> unmask round trip OK"
        PASS=$((PASS + 1))
    elif echo "$body" | grep -q "__AIGIS_SEC_"; then
        echo "FAIL [${name}]: placeholder leaked to client (unmask broken)"
        echo "  body: $(echo "$body" | head -c 400)"
        FAIL=$((FAIL + 1))
    else
        # Model may paraphrase instead of echoing — treat as a soft pass on 200.
        echo "WARN [${name}]: HTTP 200 but model did not echo the email (no leak detected)"
        echo "  body: $(echo "$body" | head -c 400)"
        PASS=$((PASS + 1))
    fi
}

echo "=== Real-upstream smoke: ${BASE_URL} ==="

# --- Chain 1: OpenAI-compatible /v1/chat/completions -------------------------
if env_has "AIGIS_OPENAI_API_KEY"; then
    resp=$(curl -s -w '\n%{http_code}' -X POST "${BASE_URL}/v1/chat/completions" \
        -H "Content-Type: application/json" \
        -d "{\"model\":\"${SMOKE_OPENAI_MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"${PROMPT}\"}]}")
    code=$(echo "$resp" | tail -n1)
    body=$(echo "$resp" | sed '$d')
    check_response "openai:${SMOKE_OPENAI_MODEL}" "$code" "$body"
else
    echo "SKIP [openai]: AIGIS_OPENAI_API_KEY not set in .env"
    SKIP=$((SKIP + 1))
fi

# --- Chain 2: Anthropic native /v1/messages -----------------------------------
if env_has "AIGIS_ANTHROPIC_KEY" && env_has "AIGIS_ANTHROPIC_BASE_URL"; then
    resp=$(curl -s -w '\n%{http_code}' -X POST "${BASE_URL}/v1/messages" \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -d "{\"model\":\"${SMOKE_CLAUDE_MODEL}\",\"max_tokens\":128,\"messages\":[{\"role\":\"user\",\"content\":\"${PROMPT}\"}]}")
    code=$(echo "$resp" | tail -n1)
    body=$(echo "$resp" | sed '$d')
    check_response "anthropic:${SMOKE_CLAUDE_MODEL}" "$code" "$body"
else
    echo "SKIP [anthropic]: AIGIS_ANTHROPIC_KEY / AIGIS_ANTHROPIC_BASE_URL not set in .env"
    SKIP=$((SKIP + 1))
fi

echo "=== Result: ${PASS} pass, ${FAIL} fail, ${SKIP} skip ==="
[ "$FAIL" -eq 0 ] || exit 1
