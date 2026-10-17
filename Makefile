VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/rhisijmuiden/iptv-portal/internal/version.Version=$(VERSION)

.PHONY: build run test
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/iptv-portal ./cmd/iptv-portal

run: build
	IPTV_PORTAL_DATA=./data ./bin/iptv-portal serve

test:
	go vet ./...
	go test ./...
