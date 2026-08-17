.PHONY: build clean test check

BIN := build/roam

build:
	go build -o $(BIN) .

clean:
	rm -rf build

test:
	go test ./...

check: test
	go vet ./...
	go fix -diff ./...
