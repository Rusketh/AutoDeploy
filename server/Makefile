BINARY := autodeploy-server
PKG    := ./cmd/autodeploy-server
# Version is stamped into the binary via -ldflags. CI sets VERSION
# from the tag that triggered the build; local builds get "dev".
VERSION ?= dev
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: build test vet lint tidy clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin

all: vet test build
