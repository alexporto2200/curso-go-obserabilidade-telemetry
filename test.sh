#!/bin/bash


# Teste 1: CEP válido
echo "1. Testando CEP válido (29902555):"
curl -X POST http://localhost:8080/cep \
  -H "Content-Type: application/json" \
  -d '{"cep": "29902555"}' \
  -w "\nHTTP Status: %{http_code}\n"
echo

# Teste 2: CEP inválido (formato)
echo "2. Testando CEP inválido (formato):"
curl -X POST http://localhost:8080/cep \
  -H "Content-Type: application/json" \
  -d '{"cep": "123"}' \
  -w "\nHTTP Status: %{http_code}\n"
echo

# Teste 3: CEP inválido (caracteres)
echo "3. Testando CEP inválido (caracteres):"
curl -X POST http://localhost:8080/cep \
  -H "Content-Type: application/json" \
  -d '{"cep": "abcdefgh"}' \
  -w "\nHTTP Status: %{http_code}\n"
echo

# Teste 4: CEP inexistente
echo "4. Testando CEP inexistente (99999999):"
curl -X POST http://localhost:8080/cep \
  -H "Content-Type: application/json" \
  -d '{"cep": "99999999"}' \
  -w "\nHTTP Status: %{http_code}\n"
echo

echo "=== Testes concluídos ==="
echo "Acesse http://localhost:9411 para visualizar os traces no Zipkin"
