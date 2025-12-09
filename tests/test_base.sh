curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {
        "role": "user", 
        "content": "My email is dangerous@coder.com and my phone is 13800138000. Can you optimize this SQL?"
      }
    ]
  }'