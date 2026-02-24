SHELL := /bin/bash

.PHONY: build build-autostart install test test-integration lint web-build web-sync

build:
	go build ./...

install:
	go install ./cmd/opencortex
	@echo Installed opencortex.
	@echo Binary directory:
	@go env GOBIN
	@echo If empty above, use:
	@go env GOPATH

build-autostart:
	go build -tags autostart ./...

test:
	go test ./...

test-integration:
	go test -v ./internal/api ./internal/e2e

lint:
	go vet ./...

web-build:
	cd web && npm install && npm run build

web-sync:
	rm -rf internal/webui/dist/*
	cp -r web/dist/* internal/webui/dist/
