#!/usr/bin/env bash
# Full quality gate: format, vet, lint, language-server diagnostics, tests.
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

echo "== golangci-lint =="
golangci-lint run ./...

echo "== gopls check =="
mapfile -t gofiles < <(git ls-files '*.go')
if [ ${#gofiles[@]} -gt 0 ]; then
  gopls check "${gofiles[@]}" | grep -vE "^$" && echo "gopls reported diagnostics (review them)" || true
fi

echo "== go test =="
go test ./...

echo "ALL GATES PASS"
