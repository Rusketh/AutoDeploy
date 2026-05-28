BINARY := autodeploy-server
PKG    := ./cmd/autodeploy-server

.PHONY: build test vet lint tidy clean

build:
	CGO_ENABLED=0 go build -trimpath -o bin/$(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin

all: vet test build
