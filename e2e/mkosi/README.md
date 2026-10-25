# Fedora container engine e2e smoke test

The e2e environment supports Fedora 44 and Fedora 45. Each image runs the same
Fish smoke test against four cases: rootful Podman, rootless Podman, rootful
Docker, and rootless Docker.

The host needs Fish, mkosi, QEMU, systemd tools used by mkosi, and the project's
normal build dependencies. From the repository root:

```sh
just e2e-prepare
just e2e-vm fedora-44
```

`e2e-vm` mounts the current Fish functions and freshly built `regview` binary
from the host into the guest with mkosi's runtime tree mounts. Changes to the
test scripts therefore take effect on the next test run without rebuilding the
Fedora image. At the VM's root prompt, load and run the matrix:

```sh
fish -c 'set -p fish_function_path /usr/local/share/regview-e2e $fish_function_path; regview_e2e_run_smoke_test'
```

To prepare only one image, pass its id, for example `just e2e-prepare fedora-45`.
Run `just e2e-vm fedora-45` to boot that image.

The smoke test calls the reusable `prepare_e2e_environment` Fish function
before testing. It enables networkd, starts D-Bus, configures subordinate IDs
for Docker rootless mode, runs `dockerd-rootless-setuptool.sh install --force`
as the test user, and checks that the guest can reach Docker Hub. Each case
runs as a separate Fish process using the shared engine-case function.
Rootless cases use `run0` with Fedora's `systemd-run0` PAM stack to create a
user session and systemd user manager for the duration of the case. The
image delegates `cpu`, `cpuset`, `io`, `memory`, and `pids` controllers to user
managers so rootless Docker can apply those resource controls. The rootless
Docker case starts and stops the installed user service. It
pulls hello-world, tags it as
`localhost:5000/library/hello-world`, serves it through regview, pulls it back,
and runs it. Docker cases also verify Docker's containerd image store, which
regview requires.

The VM is disposable; all image storage is local to it. The test uses plain
HTTP only for the guest's `localhost:5000` registry endpoint. It does not
change or retag images on the host.

Docker CE packages come from Docker's Fedora package repository. The guest
images and their container stores are disposable and isolated per Fedora
version.
