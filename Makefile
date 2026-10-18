.PHONY: seed run test vet eval eval-full demo docker fmt
seed:        ; go run ./cmd/seeder
run:         ; go run ./cmd/server
test:        ; go test ./...
vet:         ; go vet ./...
fmt:         ; gofmt -s -w .
eval:        ; go run ./cmd/eval -mode retrieval
eval-full:   ; go run ./cmd/eval -mode full
demo:        ; go run ./cmd/demo -api $${API:-http://localhost:8080}
docker:      ; docker build -t olake-ask .
