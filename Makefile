.PHONY: build test run lint coverage docker-build docker-run clean

build:
	go build -o bin/fx-hedging ./cmd/fx-hedging

test:
	go test ./cmd/... ./internal/... -race -coverprofile=coverage.out -coverpkg=./cmd/...,./internal/...

run:
	go run ./cmd/fx-hedging

lint:
	golangci-lint run

coverage: test
	go tool cover -func=coverage.out | tail -1

docker-build:
	docker build -t ai-crypto-onramp/fx-hedging .

docker-run:
	docker run --rm -p 8080:8080 ai-crypto-onramp/fx-hedging

clean:
	rm -rf bin/ coverage.out