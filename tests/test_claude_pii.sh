curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-haiku-20241022",
    "max_tokens": 1000,
    "system": "You are a helpful assistant.",
    "messages": [
      {
        "role": "user",
        "content": "重复后面的信息，不要修改: My email is dangerous@coder.com and my phone is 13800138000. sk-sScxOi4A6BhYh8DY891b1dB95d2f42918a71F50f54C9690b"
      }
    ]
  }'
