.PHONY: build run clean

build:
	go build -o bin/maple_bridge ./cmd/maple_bridge

run: build
	./bin/maple_bridge configs/config.yaml

clean:
	rm -rf bin/

deps:
	go mod tidy

test:
	go test ./...
