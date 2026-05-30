.PHONY: build run restart clean

build:
	go build -o bin/maple_bridge ./cmd/maple_bridge

run: build
	./bin/maple_bridge configs/config.yaml

restart:
	./scripts/restart.sh configs/config.yaml

clean:
	rm -rf bin/

deps:
	go mod tidy

test:
	go test ./...
