curl localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer my-gateway-key' -H 'Content-Type: application/json' \
  -d '{"model":"qwen","messages":[{"role":"user","content":"Hi"}],"stream":true}'

curl localhost:8080/api/chat -H 'Authorization: Bearer my-gateway-key' \
  -d '{"model":"qwen","messages":[{"role":"user","content":"Hi"}]}'