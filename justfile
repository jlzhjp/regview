export GO := env("GO", "go")
tags := "containers_image_openpgp,exclude_graphdriver_btrfs"
templ := "github.com/a-h/templ/cmd/templ@v0.3.1070"
golangci := "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0"
go_licenses := "github.com/google/go-licenses/v2@v2.0.1"

# Build the registry binary (default).
build: generate
    "$GO" build -tags '{{ tags }}' -o bin/regview ./cmd/regview

# Build the JS and CSS embedded in the Go binary.
frontend: frontend-deps
    pnpm build

# Install the locked frontend development tools and dependencies.
frontend-deps:
    pnpm install --frozen-lockfile

# Check frontend lint, formatting, types, and canonical Tailwind classes.
frontend-lint: frontend-deps
    pnpm lint
    pnpm fmt:check
    pnpm typecheck
    pnpm tailwind-check

# Format frontend sources, build scripts, and configuration.
frontend-fmt: frontend-deps
    pnpm fmt

# Build and start with the example configuration; forward additional CLI flags.
[positional-arguments]
run *args: build
    exec ./bin/regview --config regview.example.yaml "$@"

# Regenerate templ components and embedded third-party notices.
generate: licenses
    "$GO" run {{ templ }} generate

# Collect Go and npm production licenses in one embedded archive.
licenses: frontend
    #!/usr/bin/env bash
    set -euo pipefail
    license_stage=$(mktemp -d)
    trap 'rm -rf "$license_stage"' EXIT
    node scripts/collect-npm-licenses.ts "$license_stage/npm"
    GOFLAGS="${GOFLAGS:-} -tags={{ tags }}" "$GO" run {{ go_licenses }} save ./cmd/regview --ignore regview --save_path="$license_stage/go"
    tar -czf internal/assets/dist/licenses.tar.gz -C "$license_stage" go npm

# Format frontend, Go, and templ source files.
fmt: frontend-fmt
    gofmt -w .
    "$GO" run {{ templ }} fmt .

# Run tests with the race detector.
test: generate
    "$GO" test -race -tags '{{ tags }}' ./...

# Run Go static checks.
vet: generate
    "$GO" vet -tags '{{ tags }}' ./...

# Run the pinned linter and check Go formatting.
lint: generate frontend-lint
    "$GO" run {{ golangci }} config verify
    "$GO" run {{ golangci }} run ./...
