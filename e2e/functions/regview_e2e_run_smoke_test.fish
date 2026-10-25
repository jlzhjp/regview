function regview_e2e_run_smoke_test
    prepare_e2e_environment; or return 1

    for engine in podman docker
        for mode in rootful rootless
            echo "=== $engine $mode source ==="
            fish -c 'set -p fish_function_path /usr/local/share/regview-e2e; regview_e2e_run_engine_case $argv' \
                $engine $mode $regview_e2e_registry
            or return $status
        end
    end

    echo 'All four source cases and eight pull/run checks passed.'
end
