SHELL := /bin/bash

ROOT_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

PROTO_DIR := internal/.proto
BUF_TEMPLATE := ./buf.gen.yaml

.PHONY: all proto build test lint fmt clean

all: build

proto:
	buf export buf.build/agynio/api --output $(PROTO_DIR)
	buf generate $(PROTO_DIR) --template $(BUF_TEMPLATE)

build:
	GOFLAGS=-mod=mod go build ./...

test:
	GOFLAGS=-mod=mod go test ./...

lint:
	GOFLAGS=-mod=mod go vet ./...

fmt:
	gofmt -w $(shell find . -type f -name '*.go')

clean:
	rm -rf $(PROTO_DIR) internal/.gen
