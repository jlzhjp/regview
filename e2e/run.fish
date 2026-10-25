#!/usr/bin/env fish

if test (count $argv) -ne 1
    echo 'Usage: just e2e-vm fedora-44|fedora-45' >&2
    exit 2
end

set -l variant $argv[1]
switch $variant
    case fedora-44
        set release 44
    case fedora-45
        set release 45
    case '*'
        echo "Unknown e2e environment: $variant (use fedora-44 or fedora-45)" >&2
        exit 2
end

set -l script_dir (path dirname (status filename))
set -l repo_root (path resolve "$script_dir/..")
cd $repo_root; or exit 1

just build; or exit $status

mkosi -C e2e/mkosi \
    --release $release \
    --output $variant \
    --output-directory .build/$variant \
    --runtime-tree $repo_root/e2e/functions:/usr/local/share/regview-e2e \
    --runtime-tree $repo_root/bin:/opt/regview-e2e/bin \
    vm
