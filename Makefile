ifneq (,$(wildcard .env))
include .env
export
endif

BINARY ?= bin/sub2api-cursor-sidecar

.PHONY: help run build test install

help:
	@echo "Targets:"
	@echo "  make run      Run the sidecar locally (reads .env)"
	@echo "  make build    Build $(BINARY)"
	@echo "  make test     Run go vet and tests"
	@echo "  make install  Build and install as a systemd service (root)"

run:
	cd server && go run ./cmd/prism

build:
	cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../$(BINARY) ./cmd/prism

test:
	cd server && go vet ./... && go test ./...

install:
	./deploy/sidecar/install.sh
