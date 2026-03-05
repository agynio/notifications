SHELL := /bin/bash

ROOT_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

.PHONY: all generate build test lint fmt

all: build

generate:
	$(ROOT_DIR)/scripts/generate.sh

build:
	GOFLAGS=-mod=mod go build ./...

test:
	GOFLAGS=-mod=mod go test ./...

lint:
	GOFLAGS=-mod=mod go vet ./...

fmt:
	gofmt -w $(shell find . -type f -name '*.go')
