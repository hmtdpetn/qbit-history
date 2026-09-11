.PHONY: all test build web bench docker clean

all: test build

test:
	go vet ./...
	go test ./... -count=1

build:
	go build -o bin/history ./cmd/history
	go build -o bin/mockqb ./cmd/mockqb
	go build -o bin/benchmark ./cmd/benchmark

web:
	cd web && npm ci && npm run build

bench:
	go run ./cmd/benchmark -tasks 200 -instances 1 -hours 26
	go run ./cmd/benchmark -tasks 400 -instances 2 -hours 26
	go run ./cmd/benchmark -tasks 500 -instances 2 -hours 26

docker:
	docker compose build

clean:
	rm -rf bin .local web/dist
