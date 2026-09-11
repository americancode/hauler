# Makefile for hauler

# set shell
SHELL=/bin/bash

# set go variables
GO_FILES=./...
GO_COVERPROFILE=coverage.out
GO_VULNCHECKS=vulncheck.out
TRIVY_RESULTS=trivy.out

# set build variables
BIN_DIRECTORY=bin
DIST_DIRECTORY=dist
PROJECT=hauler.dev/go/hauler/v2
VERSION?=devel
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME?=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GOOS?=$(shell go env GOOS)
GOARCH?=$(shell go env GOARCH)
LDFLAGS=-s -w -X $(PROJECT)/internal/version.gitVersion=$(VERSION) -X $(PROJECT)/internal/version.gitCommit=$(COMMIT) -X $(PROJECT)/internal/version.gitTreeState=clean -X $(PROJECT)/internal/version.buildDate=$(BUILD_TIME)

# local build of hauler for the current platform
build:
	mkdir -p $(BIN_DIRECTORY)
	CGO_ENABLED=0 GOEXPERIMENT=boringcrypto GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIRECTORY)/hauler ./cmd/hauler/.

# local build of hauler for Linux amd64 and arm64
build-all:
	mkdir -p $(DIST_DIRECTORY)
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOEXPERIMENT=boringcrypto GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIRECTORY)/hauler-linux-$$arch ./cmd/hauler/.; \
	done

# Build release artifacts locally. Publishing is handled by CI.
release: build-all

# install depedencies
install:
	go mod tidy
	go mod download
	CGO_ENABLED=0 go install ./cmd/...

# format go code
fmt:
	go fmt $(GO_FILES)

# vet go code
vet:
	go vet $(GO_FILES)

# test go code
test:
	go test $(GO_FILES) -cover -race -covermode=atomic -coverprofile=$(GO_COVERPROFILE)

# check for vulnerabilities
vulns:
	govulncheck $(GO_FILES) > $(GO_VULNCHECKS) 2>&1 || true
	curl -fsSL -o rancher.openvex.json https://media.githubusercontent.com/media/rancher/vexhub/refs/heads/main/reports/rancher.openvex.json || true
	trivy fs --vex rancher.openvex.json --skip-files rancher.openvex.json . > $(TRIVY_RESULTS) 2>&1 || true
	rm rancher.openvex.json || true

# cleanup artifacts
clean:
	rm -rf $(BIN_DIRECTORY) $(DIST_DIRECTORY) $(GO_COVERPROFILE) $(GO_VULNCHECKS) $(TRIVY_RESULTS)
