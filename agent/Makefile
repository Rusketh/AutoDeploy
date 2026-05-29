BINARY := autodeploy-agent
VERSION ?= dev
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: build build-windows test vet tidy clean all

# Default build is for the developer's host OS (Linux on CI).
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/autodeploy-agent

# Production build target: Windows/amd64 EXE.
build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY).exe ./cmd/autodeploy-agent

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin

all: vet test build build-windows
