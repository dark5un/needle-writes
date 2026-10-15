BIN    := needle-writes
MODULE := github.com/dark5un/needle-writes
GOBIN  := $(HOME)/go/bin

.PHONY: test lint fmt modernize vuln build build-reproducible tidy

test:
	./tools/test.sh

lint:
	go vet ./...
	$(GOBIN)/golangci-lint run ./...
	$(GOBIN)/gopls check $(shell git ls-files '*.go')

fmt:
	gofmt -w .

modernize:
	go fix ./...

vuln:
	go run github.com/securego/gosec/v2/cmd/gosec@latest ./... 2>&1 | tail -5
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/$(BIN) ./cmd/$(BIN)

build-reproducible:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" -o bin/a ./cmd/$(BIN)
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" -o bin/b ./cmd/$(BIN)
	sha256sum bin/a bin/b | awk '{print $$1}' | sort -u | wc -l | grep -q '^1$$' && rm bin/a bin/b && echo "reproducible: identical"

tidy:
	go mod tidy && git diff --exit-code go.mod go.sum
