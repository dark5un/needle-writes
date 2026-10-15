#!/usr/bin/env bash
# Full quality gate: format, vet, lint, language-server diagnostics,
# modernize check, race tests with coverage, dependency hygiene.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:$HOME/go/bin"

echo "== gofmt =="
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "not gofmt-clean:"; echo "$unformatted"; exit 1
fi

echo "== go vet =="
go vet ./...
go vet -tags live ./...

echo "== golangci-lint =="
golangci-lint run ./...

echo "== gopls check =="
mapfile -t gofiles < <(git ls-files '*.go')
if [ ${#gofiles[@]} -gt 0 ]; then
  gopls check "${gofiles[@]}" | grep -vE "^$" && echo "gopls reported diagnostics (review them)" || true
fi

echo "== go fix modernize (report-only) =="
go fix -diff ./... || true

echo "== go test (race + cover) =="
go test -race -covermode=atomic -coverprofile=coverage.out ./...

echo "== coverage =="
go tool cover -func=coverage.out | tail -1

echo "== dep hygiene =="
go mod tidy
if ! git diff --quiet go.mod go.sum; then
  echo "go.mod/go.sum drifted after tidy — commit the change"; exit 1
fi
go mod verify

echo "ALL GATES PASS"
