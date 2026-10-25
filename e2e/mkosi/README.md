# Fedora container engine e2e smoke test

The e2e environment supports Fedora 44 and Fedora 45. Each image runs the same
Fish smoke test against four cases: rootful Podman, rootless Podman, rootful
Docker, and rootless Docker.

Run `just e2e-test` for script syntax and focused pull-isolation/cleanup tests
without a VM.

The host needs Fish, mkosi, QEMU, systemd tools used by mkosi, and the project's
normal build dependencies. From the repository root:

```sh
just e2e-prepare
just e2e-vm fedora-44
```

`e2e-vm` mounts the current Fish functions and freshly built `regview` binary
from the host into the guest with mkosi's runtime tree mounts. Changes to the
test scripts therefore take effect on the next test run without rebuilding the
Fedora image. At the VM's root prompt, run:

```sh
regview-e2e
```

This launcher is installed in the image at `/usr/local/bin/regview-e2e`. It
loads the mounted functions and calls `regview_e2e`, the entrypoint for all
e2e suites. Rebuild existing images with `just e2e-prepare` once to install
the launcher.

To prepare only one image, pass its id, for example `just e2e-prepare fedora-45`.
Run `just e2e-vm fedora-45` to boot that image.

The smoke test calls `prepare_e2e_environment` before testing. It enables
networkd, starts D-Bus, checks Docker Hub connectivity, and creates two users
with separate homes and disjoint subordinate UID/GID ranges:

- `e2e` owns the rootless source images.
- `e2e-pull` owns only the pull-client stores.

Both users get a rootless Docker service with the containerd image store enabled.
Rootless commands use `run0` with Fedora's `systemd-run0` PAM stack to create a
user session and systemd user manager. The image delegates `cpu`, `cpuset`,
`io`, `memory`, and `pids` controllers to user managers.

Each of the four source cases pulls hello-world from Docker Hub, tags it for
the guest registry, and serves it through regview. Rootful sources run as root;
rootless sources run as `e2e`. Docker sources also verify that Docker uses its
containerd image store, which regview requires. A named systemd unit tracks each
source worker, which replaces itself with regview so stopping the unit triggers
regview's shutdown, including its namespace child. The
rootless source Docker service is stopped after its case.

For every source, **both rootless Docker and rootless Podman** run as `e2e-pull`
to pull from regview and execute hello-world: eight pull/run checks in total.
Each client prunes its own store before pulling and verifies that no images
remain, so source layers and previous pulls cannot satisfy the download from
local storage. Execution uses `--pull=never` to require the explicitly pulled
image. Cleanup prunes and checks the store again on success or failure, before
stopping the pull user's Docker service. A cleanup failure fails the case.

The registry uses the guest's first global IPv4 address on port 5000 and listens
on all guest interfaces. This lets rootless Docker reach it even when its daemon
has a separate network namespace. HTTP is enabled for that address in the test
users' Docker configuration; Podman pulls use `--tls-verify=false`.

The VM is disposable; all image storage is local to it. The test does not change
or retag images on the host. Source stores are kept separate from client cleanup.

Docker CE packages come from Docker's Fedora package repository. The guest
images and their container stores are disposable and isolated per Fedora
version.
