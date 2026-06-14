ifneq (,$(wildcard .env))
include .env
export
endif

HOST ?= 0.0.0.0
PORT ?= 8080
PROXY_API_KEY ?= dev-change-me

.PHONY: help dev api-dev web-dev web-install dev-info api-env-check

help:
	@echo "Targets:"
	@echo "  make api-dev    Start the Go API server"
	@echo "  make api-env-check Verify env injection for the API process"
	@echo "  make web-install Install frontend dependencies"
	@echo "  make web-dev    Start the frontend dev server"
	@echo "  make dev-info   Print the effective dev login key and backend URL"

dev: api-dev

api-dev:
	@echo "Starting prism on http://$(HOST):$(PORT)"
	@echo "Dashboard login key / API bearer token: $(PROXY_API_KEY)"
	cd server && env HOST="$(HOST)" PORT="$(PORT)" PROXY_API_KEY='$(PROXY_API_KEY)' go run ./cmd/prism

api-env-check:
	@env HOST="$(HOST)" PORT="$(PORT)" PROXY_API_KEY='$(PROXY_API_KEY)' bash -lc 'printf "HOST=%s\nPORT=%s\nPROXY_API_KEY=%s\n" "$$HOST" "$$PORT" "$$PROXY_API_KEY"'

web-install:
	./scripts/web-frontend.sh install

web-dev:
	./scripts/web-frontend.sh dev --host 0.0.0.0 --port 5173

dev-info:
	@echo "Backend URL: http://$(HOST):$(PORT)"
	@echo "Dashboard login key: $(PROXY_API_KEY)"
	@echo "Bearer token: $(PROXY_API_KEY)"
