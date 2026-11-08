SHELL := /bin/sh
BIN := bin/1mate
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X 1mate/internal/version.Version=$(VERSION) \
	-X 1mate/internal/version.Commit=$(COMMIT) \
	-X 1mate/internal/version.Date=$(DATE)

.PHONY: all build web backend dev-api dev-web test test-go test-web lint vet clean

all: build

web:
	cd web && npm ci --no-audit --no-fund || cd web && npm install --no-audit --no-fund
	cd web && npm run build

backend:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/1mate

build: web backend

dev-api:
	go run ./cmd/1mate

dev-web:
	cd web && npm run dev

test: test-go test-web

test-go:
	go test ./...

test-web:
	cd web && npm run test -- --run

lint: vet
	cd web && npm run lint

vet:
	go vet ./...

clean:
	rm -rf bin internal/webui/dist web/dist
