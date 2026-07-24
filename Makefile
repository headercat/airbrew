BINARY := bin/airbrew
PKG := github.com/headercat/airbrew
GO := go

.PHONY: all build web web-build web-dev run dev test vet fmt clean tidy

all: web build

## build: Build the Go binary (assumes web/dist is populated)
build:
	$(GO) build -o $(BINARY) ./cmd/airbrew

## run: Run the server (uses placeholder dist if no SPA build)
run:
	$(GO) run ./cmd/airbrew

## dev: Run Go server with live reload via air (if installed) — fallback to run
dev:
	@if command -v air > /dev/null 2>&1; then air; else $(GO) run ./cmd/airbrew; fi

## web: Install web deps and build SPA into web/dist
web: web-build

## web-install: Install frontend deps
web-install:
	cd web && npm install

## web-build: Build the SPA into web/dist
web-build:
	cd web && npm run build

## web-dev: Vite dev server with proxy to Go API (run alongside `make run`)
web-dev:
	cd web && npm run dev

## test: Run Go tests
test:
	$(GO) test ./...

## vet: Run go vet
vet:
	$(GO) vet ./...

## fmt: Format Go code
fmt:
	$(GO) fmt ./...

## tidy: Run go mod tidy
tidy:
	$(GO) mod tidy

## clean: Remove build artifacts and local data
clean:
	rm -rf bin data airbrew.db airbrew.db-shm airbrew.db-wal .airbrew-data

.DEFAULT_GOAL := all
