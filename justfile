export GO := env("GO", "go")
tags := "containers_image_openpgp,exclude_graphdriver_btrfs"
templ := "github.com/a-h/templ/cmd/templ@v0.3.1070"

# Build the registry binary (default).
build:
    "$GO" build -tags '{{ tags }}' -o bin/regview ./cmd/regview

# Build and start with the example configuration; forward additional CLI flags.
[positional-arguments]
run *args: build
    exec ./bin/regview --config regview.example.yaml "$@"

# Regenerate the checked-in templ components.
generate:
    "$GO" run {{ templ }} generate

# Format Go and templ source files.
fmt:
    gofmt -w .
    "$GO" run {{ templ }} fmt .

# Run tests with the race detector.
test:
    "$GO" test -race -tags '{{ tags }}' ./...

# Run Go static checks.
vet:
    "$GO" vet -tags '{{ tags }}' ./...
