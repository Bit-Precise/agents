.PHONY: all build frontend generate test race lint fmt clean
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TARGET_OS ?= $(shell go env GOOS)
TARGET_ARCH ?= $(shell go env GOARCH)

all: build
build: frontend
	CGO_ENABLED=0 GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) go build -mod=readonly -trimpath -ldflags="-s -w -X main.Version=$(VERSION)" -o bin/agents ./cmd/agents

frontend:
	npm ci --prefix console
	npm run build --prefix console
generate:
	npm ci --prefix console
	npm run generate --prefix console

test:
	go test -mod=readonly -timeout=120s ./...
race:
	go test -mod=readonly -race -timeout=120s ./...
lint:
	@test -z "$$(git ls-files -z '*.go' | xargs -0 gofmt -l)" || (echo 'run make fmt'; exit 1)
	go vet -mod=readonly ./...
fmt:
	gofmt -w cmd control edge gateway internal api
clean:
	rm -r bin dist
