curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user", 
        "content": "重复后面的信息: My email is dangerous@coder.com and my phone is 13800138000."
      }
    ]
  }'