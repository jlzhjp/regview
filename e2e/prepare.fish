#!/usr/bin/env fish

set -l variants $argv
if test (count $variants) -eq 0
    set variants fedora-44 fedora-45
end

set -l script_dir (path dirname (status filename))
set -l repo_root (path resolve "$script_dir/..")
cd $repo_root; or exit 1

for variant in $variants
    switch $variant
        case fedora-44
            set release 44
        case fedora-45
            set release 45
        case '*'
            echo "Unknown e2e environment: $variant (use fedora-44 or fedora-45)" >&2
            exit 2
    end

    echo "Building $variant (Fedora $release)"
    mkosi -C e2e/mkosi \
        --release $release \
        --output $variant \
        --output-directory .build/$variant \
        --force build
    or exit $status
end
