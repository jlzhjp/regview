function regview_e2e_run_engine_case
    set -l engine $argv[1]
    set -l mode $argv[2]
    set -l registry $argv[3]
    set -l account root
    set -l workdir /root
    if test $mode = rootless
        set account e2e
        set workdir /home/e2e
    end

    set -g regview_case_log (mktemp); or return 1
    set -g regview_case_unit regview-e2e-source-$fish_pid.service
    set -g regview_case_pid
    set -g regview_case_rootless_docker false
    if test $engine = docker; and test $mode = rootless
        set -g regview_case_rootless_docker true
    end

    function interrupt_regview_e2e_case --on-signal INT --on-signal TERM
        cleanup_regview_e2e_case
        functions --erase interrupt_regview_e2e_case
        # Re-raise after cleanup so the caller observes an interrupted process.
        kill -TERM $fish_pid
    end

    function cleanup_regview_e2e_case --on-event fish_exit
        set -l result 0
        if set -q regview_case_unit
            # The worker execs regview, whose shutdown also stops its namespace child.
            systemctl stop $regview_case_unit; or set result 1
            set -eg regview_case_unit
        end
        if set -q regview_case_pid[1]
            wait $regview_case_pid 2>/dev/null
            set -eg regview_case_pid
        end
        if test "$regview_case_rootless_docker" = true
            run0 --pipe --user=e2e --chdir=/home/e2e systemctl --user stop docker.service; or set result 1
            set -g regview_case_rootless_docker false
        end
        if set -q regview_case_log
            cat $regview_case_log
            rm -f $regview_case_log
            set -eg regview_case_log
        end
        return $result
    end

    # Keep run0's descriptors as pipes: SELinux prevents init from opening a
    # caller-created temporary file as the transient service's output.
    run0 --pipe --unit=$regview_case_unit --property=KillMode=control-group \
        --user=$account --chdir=$workdir \
        fish -c 'set -p fish_function_path /usr/local/share/regview-e2e; regview_e2e_serve $argv' \
        $engine $mode $registry 2>&1 | cat >$regview_case_log &
    set -g regview_case_pid $last_pid

    set -l ready false
    for attempt in (seq 1 90)
        if curl --noproxy '*' --silent --fail --connect-timeout 2 --max-time 4 http://$registry/v2/ >/dev/null
            set ready true
            break
        end
        if not kill -0 $regview_case_pid 2>/dev/null
            break
        end
        sleep 1
    end

    set -l result 0
    if test $ready != true
        echo "Source $engine $mode did not become ready." >&2
        set result 1
    else
        curl --noproxy '*' --silent --show-error --fail \
            http://$registry/v2/library/hello-world/tags/list; or set result 1
        if test $result -eq 0
            for client in docker podman
                echo "--- Pull with rootless $client as e2e-pull ---"
                run0 --pipe --user=e2e-pull --chdir=/home/e2e-pull \
                    fish -c 'set -p fish_function_path /usr/local/share/regview-e2e; regview_e2e_pull $argv' \
                    $client $registry/library/hello-world:latest
                or begin
                    set result $status
                    break
                end
            end
        end
    end

    cleanup_regview_e2e_case; or set result 1
    functions --erase cleanup_regview_e2e_case interrupt_regview_e2e_case
    return $result
end
