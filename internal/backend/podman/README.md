# Podman adapter

This package converts local Podman storage images to OCI content. These notes
explain the ownership and conversion rules behind the implementation.

## Storage configuration

`storageconfig` selects the native resolver for rootful runs and for Podman 6 or
other major versions. A positively identified Podman 5 rootless client selects
`storageconfig/podman5`, which follows storage v1.62.2's selective inheritance.
Only compatible drivers, pull options, the legacy rootless path, and overlay
`ignore_chown_errors` pass from system defaults into implicit rootless settings.
User or explicitly selected configuration retains its own options. Explicit
regview path/driver overrides apply last; changing drivers clears old driver options.

Version detection runs `podman --remote=false version --format json`, reads
`Client.Version`, and times out after three seconds. Detection failure warns and
uses native resolution. Rootful, dry-run, and Docker-only runs skip detection.
No Podman API service is needed.

Rootless paths default to `$XDG_DATA_HOME/containers/storage` (otherwise
`~/.local/share/containers/storage`) and `$XDG_RUNTIME_DIR/containers`, with the
library's runtime fallback. Storage files/drop-ins, `CONTAINERS_STORAGE_CONF`,
`CONTAINERS_STORAGE_CONF_OVERRIDE`, `STORAGE_DRIVER`, and `STORAGE_OPTS` are supported.
An inaccessible configured additional image store makes the backend unavailable.

Rootless startup resolves the user's store before entering a user namespace.
Automatic discovery checks for an existing initialized store and can continue
with Docker if namespace setup fails. Opening storage may initialize lock/runtime
metadata. Never call `storage.Shutdown`: it can unmount layers used by the engine.

## Request and reader ownership

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

