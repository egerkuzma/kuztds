.PHONY: build run test bench lint vuln infra-up infra-down tidy

build:
	go build -o bin/kuztds-engine ./cmd/engine
	go build -o bin/kuztds-admin  ./cmd/admin

run:
	go run ./cmd/engine

test:
	go test ./...

bench:
	go test ./internal/ipindex/ -bench=. -benchmem

lint:
	golangci-lint run

vuln:
	govulncheck ./...

tidy:
	go mod tidy

# Local ClickHouse + Redis
infra-up:
	docker compose -f deploy/docker-compose.yml up -d

infra-down:
	docker compose -f deploy/docker-compose.yml down
