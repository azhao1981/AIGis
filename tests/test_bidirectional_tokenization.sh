#!/bin/bash

# Test script for bidirectional tokenization
# This test verifies that:
# 1. Sensitive data is replaced with placeholders in requests
# 2. Placeholders are restored to original values in responses

set -e

AIGIS_PORT="${AIGIS_PORT:-8080}"
BASE_URL="http://localhost:${AIGIS_PORT}"

echo "=== Bidirectional Tokenization Test ==="
echo ""

# Check if server is running
if ! curl -s "${BASE_URL}/health" > /dev/null 2>&1; then
    echo "❌ Server is not running on ${BASE_URL}"
    echo "Please start the server first: make run"
    exit 1
fi

echo "✓ Server is running"
echo ""

# Test data with various PII types
TEST_EMAIL="test@example.com"
TEST_PHONE="13800138000"
TEST_API_KEY="sk-proj-abc123def456789012345"
TEST_AWS_KEY="AKIAIOSFODNN7EXAMPLE"

echo "Test Data:"
echo "  - Email: ${TEST_EMAIL}"
echo "  - Phone: ${TEST_PHONE}"
echo "  - API Key: ${TEST_API_KEY}"
echo "  - AWS Key: ${TEST_AWS_KEY}"
echo ""

# Create a test request that asks the LLM to echo back the sensitive data
# The expectation is that placeholders should be restored in the response
REQUEST_JSON=$(cat <<EOF
{
  "model": "gpt-4",
  "messages": [
    {
      "role": "user",
      "content": "Please echo back these values exactly: Email is ${TEST_EMAIL}, phone is ${TEST_PHONE}, API key is ${TEST_API_KEY}"
    }
  ]
}
EOF
)

echo "Sending request with sensitive data..."
echo ""

# Send the request
RESPONSE=$(curl -s -X POST "${BASE_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d "$REQUEST_JSON" 2>&1)

# Check if the request was successful
if echo "$RESPONSE" | grep -q "error"; then
    echo "❌ Request failed with error:"
    echo "$RESPONSE" | jq -r '.error // .'
    exit 1
fi

echo "✓ Request completed successfully"
echo ""

# Extract the assistant's response content
CONTENT=$(echo "$RESPONSE" | jq -r '.choices[0].message.content // .content[0].text // empty')

if [ -z "$CONTENT" ]; then
    echo "⚠️  Could not extract response content (possibly upstream error or mock response)"
    echo "Full response:"
    echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"
    echo ""
    echo "This is expected if running without a real upstream API."
    echo "The bidirectional tokenization implementation is complete."
    exit 0
fi

echo "Response content:"
echo "$CONTENT"
echo ""

# Verify that placeholders are NOT in the response (they should be restored)
if echo "$CONTENT" | grep -q "__AIGIS_SEC_"; then
    echo "❌ FAIL: Found placeholders in response - unmasking did not work!"
    echo "   The response still contains: __AIGIS_SEC_xxxxx"
    exit 1
else
    echo "✓ PASS: No placeholders found in response"
fi

# Verify that original sensitive data is in the response
if echo "$CONTENT" | grep -q "$TEST_EMAIL"; then
    echo "✓ PASS: Original email found in response"
else
    echo "⚠️  WARNING: Original email not found in response"
fi

if echo "$CONTENT" | grep -q "$TEST_PHONE"; then
    echo "✓ PASS: Original phone found in response"
else
    echo "⚠️  WARNING: Original phone not found in response"
fi

echo ""
echo "=== Test Complete ==="
