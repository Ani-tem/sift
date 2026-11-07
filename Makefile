.PHONY: build test install

build:
	mkdir -p bin
	swiftc -O helper/extract.swift -o bin/sift-extract
	go build -o bin/sift ./cmd/sift

test:
	go vet ./...
	go test -race ./...

# Puts sift and its helper on your PATH.
install: build
	cp bin/sift bin/sift-extract $(HOME)/go/bin/
