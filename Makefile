BINARY := cc-proxy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/sanketsudake/cc-proxy/internal/version.Version=$(VERSION) \
           -X github.com/sanketsudake/cc-proxy/internal/version.Commit=$(COMMIT)

.PHONY: build install run test lint tidy up down clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

install:
	go install -ldflags "$(LDFLAGS)" .

run: build
	./$(BINARY)

test:
	go test -race ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

up:
	docker compose -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down

clean:
	rm -f $(BINARY)
