.PHONY: build test run lint cover docker-build docker-run docker-up docker-down clean migrate-up migrate-down

build:
	go build -o bin/fx-hedging ./cmd/fx-hedging

test:
	go test ./internal/... -race -coverprofile=coverage.out -coverpkg=./internal/...

run:
	go run ./cmd/fx-hedging

lint:
	golangci-lint run

cover: test
	go tool cover -func=coverage.out | tail -1

docker-build:
	docker build -t ai-crypto-onramp/fx-hedging .

docker-run:
	docker run --rm -p 8080:8080 -p 9090:9090 ai-crypto-onramp/fx-hedging

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

clean:
	rm -rf bin/ coverage.out

migrate-up:
	go run ./cmd/migrate --up

migrate-down:
	go run ./cmd/migrate --down