.PHONY: help build test vet up down logs reset-store clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Build all three binaries into ./bin
	go build -o bin/exchange  ./cmd/exchange
	go build -o bin/oe-client ./cmd/oe-client
	go build -o bin/dc-client ./cmd/dc-client

test: ## Run the drill suite
	go test ./...

vet: ## Static checks
	go vet ./...

up: ## Start the stack
	docker compose up -d --build

down: ## Stop the stack
	docker compose down

logs: ## Follow the wire
	docker compose logs -f

reset-store: ## Wipe stored sequence numbers and start clean
	docker compose down -v

clean: ## Remove build output and local store/log dirs
	rm -rf bin store logs
