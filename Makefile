.PHONY: build run test docker-up docker-up-gateway docker-down generate fmt vet

BINARY := bin/gateway

build:
	go build -o $(BINARY) ./cmd/gateway

run: build
	./$(BINARY)

test:
	go test ./...

# Full stack: gateway + Ollama (requires Docker Hub access to pull the image)
docker-up:
	docker-compose --profile full up -d --build

# Gateway only; point OLLAMA_BASE_URL at an external Ollama (e.g. host.docker.internal)
docker-up-gateway:
	docker-compose up -d --build gateway

docker-down:
	docker-compose down

generate:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		internal/api/pb/gateway.proto

fmt:
	gofmt -l -w .

vet:
	go vet ./...
