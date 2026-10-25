export GO := env("GO", "go")
tags := "containers_image_openpgp,exclude_graphdriver_btrfs"
templ := "github.com/a-h/templ/cmd/templ@v0.3.1070"
golangci := "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0"
go_licenses := "github.com/google/go-licenses/v2@v2.0.1"

# Build the registry binary (default).
build: generate
    "$GO" build -tags '{{ tags }}' -o bin/regview ./cmd/regview

# Build the JS and CSS embedded in the Go binary.
ui: ui-deps
    pnpm build

# Install the locked UI development tools and dependencies.
ui-deps:
    pnpm install --frozen-lockfile

# Check UI lint, formatting, types, and canonical Tailwind classes.
ui-lint: ui-deps
    pnpm lint
    pnpm fmt:check
    pnpm typecheck
    pnpm tailwind-check

# Format UI sources, build scripts, and configuration.
ui-fmt: ui-deps
    pnpm fmt

# Build and start with the example configuration; forward additional CLI flags.
[positional-arguments]
run *args: build
    exec ./bin/regview --config regview.example.yaml "$@"

# Prepare one or both supported Fedora e2e images (defaults to both).
e2e-prepare *args:
    fish e2e/prepare.fish {{ args }}

# Boot a prepared Fedora VM with live-mounted scripts and binary.
e2e-vm variant:
    fish e2e/run.fish {{ variant }}

# Regenerate templ components and embedded third-party notices.
generate: licenses
    "$GO" run {{ templ }} generate

# Collect Go and npm production licenses in one embedded archive.
licenses: ui
    #!/usr/bin/env bash
    set -euo pipefail
    license_stage=$(mktemp -d)
    trap 'rm -rf "$license_stage"' EXIT
    node scripts/collect-npm-licenses.ts "$license_stage/npm"
    GOFLAGS="${GOFLAGS:-} -tags={{ tags }}" "$GO" run {{ go_licenses }} save ./cmd/regview --ignore regview --save_path="$license_stage/go"
    tar -czf internal/assets/dist/licenses.tar.gz -C "$license_stage" go npm

# Format UI, Go, and templ source files.
fmt: ui-fmt
    gofmt -w .
    "$GO" run {{ templ }} fmt .

# Run tests with the race detector.
test: generate
    "$GO" test -race -tags '{{ tags }}' ./...

# Run Go static checks.
vet: generate
    "$GO" vet -tags '{{ tags }}' ./...

# Run the pinned linter and check Go formatting.
lint: generate ui-lint
    "$GO" run {{ golangci }} config verify
    "$GO" run {{ golangci }} run ./...
