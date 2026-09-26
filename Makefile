# SPDX-FileCopyrightText: 2026 The ucs-exporter authors
#
# SPDX-License-Identifier: GPL-3.0-only

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BRANCH   ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
IMAGE    ?= ucs-exporter
LDFLAGS  := -s -w \
	-X github.com/prometheus/common/version.Version=$(VERSION) \
	-X github.com/prometheus/common/version.Revision=$(REVISION) \
	-X github.com/prometheus/common/version.Branch=$(BRANCH) \
	-X github.com/prometheus/common/version.BuildUser=$(USER) \
	-X github.com/prometheus/common/version.BuildDate=$(shell date -u +%Y%m%d-%H:%M:%S)

.PHONY: all build test vet fmt golden docs docker run-fake clean

all: vet test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ucs-exporter ./cmd/ucs-exporter

test: vet
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

fmt:
	gofmt -w .

# Regenerate golden metric output after an intended change; review the diff.
golden:
	go test ./internal/modules -run TestGolden -update

# Regenerate the metrics reference.
docs:
	go run ./cmd/ucs-exporter modules --markdown > docs/metrics.md

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) -t $(IMAGE):$(VERSION) .

# Serve the synthetic fixtures as a fake UCS Manager on http://127.0.0.1:8080.
run-fake:
	go run ./tools/fakeucsm -fixtures testdata/fixtures/synthetic -listen 127.0.0.1:8080

clean:
	rm -rf bin
