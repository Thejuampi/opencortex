SHELL := /bin/bash

.PHONY: build test test-integration lint web-build web-sync

build:
	go build ./...

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
