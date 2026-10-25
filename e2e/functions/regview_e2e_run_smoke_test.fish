function regview_e2e_run_smoke_test
    prepare_e2e_environment; or return 1

    for engine in podman docker
        for mode in rootful rootless
            echo "=== $engine $mode ==="
            if test $mode = rootless
                run0 --user=e2e --chdir=/home/e2e \
                    fish -c 'set -p fish_function_path /usr/local/share/regview-e2e $fish_function_path; regview_e2e_run_engine_case $argv[1] $argv[2]' \
                    $engine $mode
                or return $status
            else
                fish -c 'set -p fish_function_path /usr/local/share/regview-e2e $fish_function_path; regview_e2e_run_engine_case $argv[1] $argv[2]' $engine $mode
                or return $status
            end
        end
    end

    echo 'All four engine/mode cases passed.'
end
