# regview

Expose local Docker and Podman images through a read-only OCI/Docker registry,
with a separate plain HTML admin viewer.

Requires Linux and Go 1.27.3 or newer. Building frontend assets also requires
Node.js 22.18+ and pnpm 12.15.0. Development recipes use just. Docker must
use the **containerd image store**; classic graph-driver image storage is unsupported. Podman uses its
configured containers storage, without requiring a Podman API service.

## Build and run

Quick start using the example configuration (automatic backend discovery):

```sh
just run
just run --backends docker
just run --dry-run
```

`just run` builds the binary first and forwards additional CLI flags. You can
override settings or select another file with `just run --config your-config.yaml`.

To build and run directly:

```sh
just build
./bin/regview --backends docker,podman --hostnames docker.io,quay.io
```

If the checkout has no readable Git metadata, build with
`GOFLAGS=-buildvcs=false just build`.

Serving is the default command. The registry listens on `127.0.0.1:5000` and
the admin viewer on `http://127.0.0.1:8080`. Both use HTTP and Go's built-in
`http.ServeMux`. There is currently no authentication or TLS.

Run `./bin/regview licenses` to print the third-party license notices embedded
in this build, similar to `gh licenses`. It works offline, writes to standard
output, and does not load configuration, open image stores, or start listeners.
Notices appear in one alphabetically sorted list. For interactive reading, use
`./bin/regview licenses | less`.

Omit `backends` to discover all usable local Docker and Podman backends at startup.
Discovery opens each existing store and checks that image metadata is readable;
an initialized store with no images is usable. Missing, inaccessible, or unsupported
backends are skipped with a warning. If none are usable, startup fails. Discovery
does not create a new Podman image store, and it does not run again in the background.

Select explicitly with `--backends docker`, `--backends podman`, or
`--backends docker,podman`. An explicitly empty list is invalid.
Repeat `--hostnames` or supply a comma-separated list. At least one hostname
is required. All explicitly selected backends must be available at startup.

```sh
./bin/regview --config regview.example.yaml --dry-run
./bin/regview --config regview.example.yaml
```

`--dry-run` validates and prints the resolved configuration, then exits. It does
not contact engines, initialize storage, export layers, or bind listeners.
It does not guarantee that configured paths or ports will be usable at runtime.
With automatic selection, `backends: null` in its JSON output means discovery is
pending; dry-run does not probe backend availability.

## Host matching

The incoming HTTP `Host` must **exactly** match the registry authority in the
local image name and the configured `hostnames` allowlist. Ports are significant.
Forwarded host headers are ignored.

For example, `quay.io/team/api:v1` appears at
`/v2/team/api/manifests/v1` only with `Host: quay.io`. A request with
`Host: localhost:5000` will not find it. Short local names such as
`alpine:latest` expand to `docker.io/library/alpine:latest`.

```sh
curl -H 'Host: quay.io' http://127.0.0.1:5000/v2/_catalog
curl -H 'Host: quay.io' http://127.0.0.1:5000/v2/team/api/tags/list
```

To use a normal registry client, route the image's authority to regview, preserving
the Host header, or tag images with an authority that already routes here.
Clients need to allow HTTP for that registry. Docker Hub clients commonly connect
to `registry-1.docker.io`; exposing `docker.io` does not create a hostname alias.
A proxy must explicitly handle that mapping if needed.

For a straightforward local example, tag an existing image
`localhost:5000/team/api:v1`, expose `localhost:5000`, and pull that same reference
from another client configured to permit HTTP.

When both backends contain different manifests under the same tag, regview logs
a warning and serves Docker's image. The viewer shows both records and marks
the selected record and conflict. Repositories and tags otherwise form a union.
Content is scoped to the requested host and repository, including digest requests.

## Storage and identity

- **Root:** use the system Docker daemon and system Podman storage.
- **Normal user:** use that user's rootless engines/stores. Membership in the
  Docker group does not select the system daemon automatically.
- **Docker:** query the local engine's `/info` endpoint to verify the containerd
  image store and discover its socket and namespace. The default engine socket
  is `/var/run/docker.sock` for root and `$XDG_RUNTIME_DIR/docker.sock` for users
  (falling back to `/run/user/<uid>/docker.sock`).
  Docker CLI contexts and remote endpoints are not automatically selected; use
  regview's explicit socket overrides for nonstandard local installations.
- **Podman:** configuration is resolved through Go libraries. For rootless
  startup, regview runs `podman --remote=false version --format json` and reads
  `Client.Version`, with a three-second timeout. Only a detected Podman 5 client
  selects the self-contained compatibility resolver. Podman 6 and other major
  versions use the storage library's native resolver. Rootful runs always use
  the native resolver and skip version detection. If the executable is absent or
  detection fails, regview logs a warning and uses native resolution; explicitly
  selected storage configuration can still be used without the Podman executable.
  No Podman API service is required. Dry-run and Docker-only mode never probe
  the Podman version.

  The Podman 5 resolver follows storage v1.62.2's selective rootless inheritance:
  system defaults contribute the compatible driver, pull options, legacy
  rootless path, and overlay `ignore_chown_errors` setting. Rootful paths,
  additional stores, mount options, and transient-store settings are not inherited.
  User or explicitly selected rootless configuration retains its own options.
  Native resolution applies the current storage library's configuration rules.

  When paths are not configured, rootless graphroot defaults to
  `$XDG_DATA_HOME/containers/storage` (otherwise `~/.local/share/containers/storage`),
  and runroot to `$XDG_RUNTIME_DIR/containers`, with the library's runtime fallback
  when that variable is absent. Storage configuration, drop-ins,
  `CONTAINERS_STORAGE_CONF`, `CONTAINERS_STORAGE_CONF_OVERRIDE`, `STORAGE_DRIVER`,
  and `STORAGE_OPTS` remain supported. Regview's explicit path and driver flags
  take precedence after the selected resolver runs.
  Rootless runs re-exec into a user namespace using the storage library's helpers;
  `newuidmap`, `newgidmap`, and subordinate UID/GID mappings may be required.
  Automatic discovery can continue with Docker if namespace setup is unavailable.
  An inaccessible additional image store selected by the resolver makes the
  Podman backend unavailable; discovery does not silently discard it.

Explicit overrides are available for custom installations:

```sh
./bin/regview --hostnames registry.example:5000 \
  --docker-socket /run/containerd/containerd.sock --docker-namespace moby
```

`--docker-socket` bypasses Docker engine discovery. It must point to the containerd
instance containing Docker's images. Podman overrides are `--podman-graphroot`,
`--podman-runroot`, and `--podman-driver`. Changing drivers discards the previous
driver's options; normally use the driver's existing `storage.conf` configuration.

The registry never pushes, deletes, or retags source images. Podman library access
can initialize runtime/lock metadata and create temporary export files. It does
not shut down the shared storage driver or unmount engine layers on exit.

## Request-local resolution and Podman exports

Every registry listing, content lookup, and admin page queries current backend
state. There is no saved image inventory, tag cache, shared prepared-artifact
cache, or background inventory refresh. A backend listing failure is reported
immediately; no previous inventory is used.

Only verified Podman layer readers are cached, for a short TTL. The cache keeps
open temporary readers and their verified sizes in this process; it creates no
named blob directory or restart-persistent index. Image records, generated
manifests, and repository-membership decisions remain request-local.

Each request pins the selected image's storage ID and target descriptor. Retagging
can affect the next request, but cannot substitute a different image into a
request already in progress. Deletion may make a request fail; regview does not
switch a selected Docker tag to a Podman image when that happens.

Docker manifests and content retain their existing bytes and digests. Blobs are
read through the local containerd content API without copying them to application
storage.

For Podman, the official `go.podman.io/image/v5/storage` reader supplies the
original configuration and `LayerInfosForCopy`. Regview generates OCI manifests
using the uncompressed layer digests and sizes supplied by that metadata. It does
not reconstruct known-size layers merely to produce a manifest. If a layer's size
is unknown, only that layer is reconstructed and measured on a cache miss. A
cached verified reader also supplies its measured size, so subsequent requests
can avoid repeating that work. Blob resolution does not measure unrelated
unknown-size layers.

Layers use `application/vnd.oci.image.layer.v1.tar`. An actual layer download
exports the requested layer on a cache miss, verifies its full uncompressed
digest and size before publication, then serves it using a seekable reader.
Already seekable official readers are reused. Other readers are spooled to
immediately unlinked temporary files. The official Podman reader can itself
spool layers to temporary files; retaining an open reader retains its disk space.

Each download gets a lease and an independent section reader. Completing or
canceling one request releases its lease without closing a file used by another
request. Expiry or capacity eviction prevents new leases; active downloads can
finish before the underlying reader closes. Idle readers expire even when no
further requests arrive. Shutdown retires retained readers and cancels loads.
Shared loads have a five-minute timeout and are canceled when all waiters leave;
the official reader may finish spooling before it observes cancellation. Late
results are disposed rather than retained.

HEAD and conditional requests use metadata without exporting known-size layers.
Unknown-size layers may still require measurement. Ranges and If-Range are
supported. A cached range download reuses the verified file. A cache miss
reconstructs and verifies the complete layer before sending just the requested
range. No blob cache hit bypasses current host/repository resolution.

The original configuration blob is preserved. Generated Podman OCI manifests
usually have different digests from the stored manifests. Digest lookup regenerates
candidate manifests only within the requested host and repository. Original
digests are never aliased to modified bytes. Signatures and attestations tied to
the original manifest are not carried over as verified metadata.

OCI and Docker schema-2 manifests and indexes are supported. Docker indexes retain
their original bytes even if some platform manifests are absent locally; requests
for missing content return an error. Podman indexes are rebuilt from locally
available instances, omitting absent instances. Legacy schema-1 images and a
referrers/signature API are not supported.

The tradeoff is retaining temporary disk space and file descriptors briefly to
avoid repeated reconstruction and verification. Metadata resolution still runs
on every request, preserving live retagging/deletion behavior. Uncompressed
layers still increase network traffic.

The default Podman reader TTL is **2 minutes**, with **1 GiB** of retained blobs
and **64 entries**. TTL starts after verification and does not slide on access.
Capacity eviction uses least recent use. Oversized blobs are served without
retention. Limits apply to lookup-eligible cached entries; active evicted leases
and in-flight exports can temporarily use additional disk space and descriptors.

```sh
./bin/regview --backends podman --hostnames quay.io \
  --podman-blob-cache-ttl 2m \
  --podman-blob-cache-max-bytes 1073741824 \
  --podman-blob-cache-max-entries 64
```

Set `--podman-blob-cache-ttl 0` to disable reader reuse and return to request-only
export ownership. Failed exports and unverified bytes are never cached. Docker
layer bytes, inventories, and generated manifests are not cached.

## Configuration

Configuration uses Viper with precedence: flags, environment variables,
configuration file, defaults. See [regview.example.yaml](regview.example.yaml).
Unknown configuration fields are rejected.

Without `--config`, regview looks for `regview.yaml` in
`$XDG_CONFIG_HOME/regview` (otherwise `~/.config/regview`) for normal users, or
`/etc/regview` for root. Environment variables use the `REGVIEW_` prefix and
underscores, for example:

```sh
REGVIEW_BACKENDS=podman REGVIEW_HOSTNAMES=quay.io ./bin/regview
```

No upstream registry fetches are performed. The old persistent cache directory
and inventory refresh options remain unsupported. Podman reader-cache settings
use the same flag/environment/file precedence as other settings.

## Development

```sh
pnpm install --frozen-lockfile
pnpm build     # Build frontend assets for Go embedding
pnpm typecheck # Check frontend and Vite configuration TypeScript
pnpm dev       # Watch frontend sources and rebuild assets
just frontend-lint # Check frontend lint, formatting, types, and Tailwind classes
just frontend-fmt  # Format frontend sources, scripts, and configuration
just fmt       # Format frontend, Go, and templ source files
just licenses  # Build frontend and archive all dependency notices
just generate  # Run licenses and regenerate templ components
just test      # Generate assets/notices and run race-enabled tests
just vet
just lint      # Validate configuration, run linters, and check Go formatting
```

`just lint` includes `just frontend-lint`: Oxlint checks TypeScript correctness,
Oxfmt checks formatting, TypeScript checks types, and `scripts/tailwind-check.ts`
reports non-canonical Tailwind classes with file, line, column, and a suggested
replacement. The checker scans static class attributes in `.templ` and TypeScript
sources, arbitrary-value class strings in TypeScript, and CSS `@apply` directives.
Dynamic expressions are skipped. It loads the project's CSS design system, so
custom classes remain valid. Tailwind and its Node/Vite packages are pinned to
4.3.3 because the canonicalization API is unstable; review the checker when
upgrading them. These tools are development dependencies and stay out of the
production license collection. The same checks are available through `pnpm lint`,
`pnpm fmt:check`, `pnpm typecheck`, and `pnpm tailwind-check`; `pnpm fmt` applies
frontend formatting. Generated assets are excluded from linting and formatting.

`just lint` also runs golangci-lint v2.15.0 with the project Go toolchain, without a
separate installation or changes to application dependencies. The configuration
in [.golangci.yml](.golangci.yml) enables the standard linters plus checks for
HTTP resource handling, error handling, suspicious code, and modernization
(`modernize`). It also checks `gofmt` formatting and excludes generated code,
including templ components. Run `just fmt` to format authored source files.

The justfile uses `containers_image_openpgp,exclude_graphdriver_btrfs` to avoid
optional GPGME and btrfs C dependencies. The default build supports overlay and
vfs storage. Builds requiring the btrfs driver should remove the latter tag and
install the driver's development dependencies. Go builds outside the justfile
need the same tags unless those optional dependencies are installed.

The browser uses templ-generated HTML, HTMX, Tailwind CSS, and Pixelarticons.
Frontend files are organized as follows:

```text
frontend/
  src/
    app.ts              # Browser behavior and JS entry point
    app.css             # Tailwind styles and icon imports
scripts/
  collect-npm-licenses.ts # Collect installed npm license and notice files
internal/admin/
  admin.go              # HTTP routes, including /assets/
  views.templ           # Admin HTML components
internal/assets/
  assets.go             # Embedded filesystem shared by HTTP and CLI consumers
  licenses.go           # Read and print bundled third-party notices
  dist/                 # Generated files (gitignored)
    frontend/           # Vite output: app.js, app.css, and font
    licenses.tar.gz     # Combined Go and npm license collection
```

Vite builds `frontend/src/app.ts` into `internal/assets/dist/frontend/`.
`just licenses` builds the frontend and collects dependency notices into
`internal/assets/dist/licenses.tar.gz`. The `assets` package embeds the generated
files and provides license output to the CLI; the `admin` package serves the files
at `/assets/`. The entry files are `/assets/frontend/app.js` and
`/assets/frontend/app.css`. Source files stay
outside the embedded directory. `just build`, `just test`,
`just vet`, and `just lint` install locked dependencies and build the frontend
first; direct Go commands require `just generate` beforehand, including on a fresh
checkout. The frontend and Vite configuration use strict TypeScript, and
`pnpm build` runs type checking before bundling.
After editing frontend or templ sources, run `pnpm build`
and `just generate` as needed; rebuild/restart Go to see embedded asset changes.
`pnpm dev` watches and rebuilds assets; it does not start a standalone web server.
The viewer provides system/light/dark themes, a saved theme preference, keyboard
focus styles, a skip link, labeled controls, and live update/error announcements.
Its compact interface uses square controls, thin borders, and a self-hosted IBM
Plex Mono variable WOFF2 font. The prebuilt font package's telemetry install script
and unused telemetry dependency are disabled in `pnpm-workspace.yaml`.
All browser assets are served locally. License notices are generated from the
installed dependencies rather than maintained by hand:

- `just licenses` runs `node scripts/collect-npm-licenses.ts <staging-directory>`;
  the script uses `pnpm licenses list --prod --json` to discover installed
  npm production dependencies, including transitive and optional dependencies.
  Development dependencies are excluded.
  `pnpm-workspace.yaml` marks Pixelarticons' React peer as optional because this
  app uses its SVGs only; React is not installed or included in the notices.
  The script copies root license, copying, and notice files into the archive's
  `npm/<package>@<version>/` directories. `npm/index.json` records package
  names, versions, declared licenses, and copied filenames without local paths.
  Packages shipping no root notice file produce a warning and a metadata entry.
- The same `just licenses` recipe runs pinned `go-licenses/v2@v2.0.1 save` against `./cmd/regview` using the application
  build tags. It collects Go license texts, notices, and any source files the
  tool requires under `go/` in the same archive. Archiving preserves nested Go
  modules and hidden files that directory embedding would otherwise omit.
  The project's own `regview` packages are excluded; their third-party
  dependencies are still scanned. This step requires `tar`.
- `just generate` runs `just licenses` and regenerates templ components;
  `just build` runs this full generation step before compiling. The resulting
  binary serves the combined archive at `/assets/licenses.tar.gz`, and
  `regview licenses` prints all notices in one alphabetical list.
  Plain `pnpm build` and `pnpm dev` rebuild only `dist/frontend/`, preserving the
  sibling license archive. Run `just generate` before a direct Go build intended
  for distribution so that notices match the current dependencies.

The collectors cover npm production dependencies and the selected Go dependency graph.
As go-licenses reports, native code and system-library dependencies require
separate review; these tools do not discover those dependencies automatically.

Vite/Rolldown reports an `EVAL` warning for HTMX 2.0.11's `internalEval` helper
(exposed as `htmx._`). Direct eval can access local scope, which complicates
bundling and minification. The viewer disables HTMX's eval-dependent features
using `allowEval: false`; its requests and swaps do not need them. This runtime
setting does not remove the eval expression from the dependency, so the static
build warning remains. Replacing it with indirect eval would change HTMX's scope
semantics; the dependency is left intact and the warning is not suppressed.
See [Rolldown's explanation](https://rolldown.rs/guide/troubleshooting#avoiding-direct-eval)
and [HTMX configuration](https://htmx.org/docs/#config).

The code is separated into storage adapters, immutable image/content values,
catalog merging and lookup, registry protocol, admin presentation, configuration,
and application lifecycle. Tests cover host/repository isolation, Docker conflict
priority, manifest bytes/digests, partial indexes, range and conditional requests,
HEAD and conditional responses, write rejection, configuration precedence, live
retagging/deletion, Podman metadata-only manifest generation, unknown-size
measurement, digest isolation, and temporary-file cleanup.

The generic `internal/ttlcache` package owns TTL, leases, concurrent load
coalescing, capacity, and disposal; it has no dependency on images or HTTP. The
Podman adapter supplies verified readers, byte weights, and pinned-source loads.
Measured sizes reuse that same reader entry rather than maintaining a second
cache. Tests cover expiry with active readers, idle cleanup, cancellation of
individual/all waiters, eviction, oversized values, failure retries, and cached
reader isolation from live repository resolution.

Podman configuration resolution is injected as a function through the backend's
options. `internal/backend/podman/storageconfig` owns native resolution and
startup selection; its `podman5` subpackage owns all compatibility behavior and
its tests. The image reader and application do not implement compatibility rules.
