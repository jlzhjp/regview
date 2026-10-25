# Working on regview

## Go style and design

Follow idiomatic Go first. Prefer small functions and explicit data flow;
functional patterns are useful when they simplify the code. Avoid adding layers,
interfaces, or generic helpers without a concrete need. Keep errors meaningful
and resource ownership visible.

Keep README.md user-facing, with only project information, setup/configuration,
and an explanation of how the program works. Put contributor instructions here,
package design notes beside their code, and local invariants in comments.

Use Context7 MCP for current library, framework, SDK, API, CLI, and cloud-service
documentation. Resolve the library ID first, then query one concept at a time.
Pure refactoring, business-logic debugging, and code review do not require it.

## Build and checks

Use the justfile as the source of truth for commands and pinned development tools.

```sh
just build          # Generate assets/notices/templates and compile bin/regview
just run --dry-run  # Build, then run with regview.example.yaml and extra flags
just fmt            # Format UI, Fish, Go, and templ sources
just test           # Generate assets and run all Go tests with the race detector
just vet
just lint           # Generate assets; check UI, Fish, and Go
```

Go builds use `containers_image_openpgp,exclude_graphdriver_btrfs` to avoid
optional GPGME and btrfs C dependencies. Overlay and vfs are supported. For a
btrfs build, remove the latter tag and install the driver's development libraries.
If Git metadata is unreadable, use `GOFLAGS=-buildvcs=false just build`.

Direct Go commands require generated embedded files first (`just generate` on a
fresh checkout), and the same build tags:

```sh
go test -race -tags containers_image_openpgp,exclude_graphdriver_btrfs ./...
go vet -tags containers_image_openpgp,exclude_graphdriver_btrfs ./...
```

Use existing behavioral tests to check refactors. Add focused tests for discovered
bugs and changed behavior. Preserve host/repository isolation, Docker conflict
priority, pinned records during retagging/deletion, original Docker bytes,
Podman verification, and cleanup/cancellation of readers and leases.
See e2e/mkosi/README.md for the Fedora VM workflow.

## Package boundaries

- `cmd/regview`: CLI and rootless startup.
- `internal/config`: resolve and validate settings once.
- `internal/app`: backend discovery and listener lifecycle.
- `internal/image`: shared image/content values and manifest graph traversal.
- `internal/catalog`: fresh inventories, conflict selection, scoped lookup.
- `internal/backend/docker`: Docker discovery and containerd reads.
- `internal/backend/podman`: OCI conversion and verified layer exports; see its README.
- `internal/backend/podman/storageconfig`: resolver selection; Podman 5 compatibility
  belongs entirely to the `podman5` subpackage.
- `internal/ttlcache`: generic leases, concurrent load coalescing, expiry, eviction,
  and disposal. Keep it independent of images and HTTP.
- `internal/registry`: read-only registry protocol.
- `internal/admin`: templ HTML and viewer routes.
- `internal/assets`: generated embedded files, shared by HTTP and license CLI output.

Inventories, generated manifests, and repository-membership decisions belong to
one request. Do not introduce a stale-inventory fallback or background refresh.
Backend connections and verified immutable readers may be shared. Preparation
must use the selected record's immutable ID and descriptor, never resolve its tag
again. Do not fall back to another backend after a selected tag fails preparation.

## UI and generated files

Edit `ui/src/app.ts`, `ui/src/app.css`, and
`internal/admin/views.templ`. Do not edit `views_templ.go` or
`internal/assets/dist/` by hand. Vite emits embedded JS/CSS/fonts to
`dist/ui/`; `just generate` also regenerates templ components and notices.
Rebuild/restart Go after changing embedded assets. `pnpm dev` watches and rebuilds
assets; it does not start a web server.

`just ui-lint` checks Oxlint, Oxfmt, TypeScript, and canonical Tailwind
classes. `just ui-fmt` formats authored UI files. The Tailwind checker
uses an unstable canonicalization API, so keep Tailwind and its Node/Vite packages
pinned together and review the checker when upgrading.

`just fish-lint` checks Fish formatting; `just fish-fmt` formats the scripts and
VM launcher.

All browser assets are local. Preserve accessible controls, focus styles, the skip
link, live announcements, and system/light/dark themes with optional saved storage.
HTMX eval-dependent features are disabled in the HTML. Its dependency still emits
an EVAL bundler warning; do not patch eval semantics or suppress the warning.

## Dependency notices

`just licenses` builds UI assets and writes
`internal/assets/dist/licenses.tar.gz`. The npm collector reads installed
production dependencies through pnpm and copies root license/copying/notice files
with a path-free `npm/index.json`. The pinned go-licenses tool collects the Go
build graph under `go/`, excluding regview's own packages. Keep required source
files and hidden files in the archive; the CLI prints only notices.

Plain `pnpm build` preserves the sibling license archive. Run `just generate`
before a distribution build so notices match dependencies. Native/system-library
licenses need separate review; collectors do not discover them.

The font package's telemetry install script/dependency are disabled in
`pnpm-workspace.yaml`. Pixelarticons' React peer is optional because only SVGs are
used; keep React out of runtime dependencies and notices.
