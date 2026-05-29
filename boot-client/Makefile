BINARY := autodeploy-boot
PKG    := ./cmd/autodeploy-boot
VERSION ?= dev
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: build test vet tidy clean

# Linux/amd64 static binary; suitable for embedding in the initramfs.
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin

all: vet test build
