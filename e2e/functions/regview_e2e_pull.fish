function regview_e2e_pull
    set -l engine $argv[1]
    set -l image $argv[2]
    if test (id -un) != e2e-pull
        echo 'Pull verification must run as e2e-pull.' >&2
        return 1
    end

    set -g regview_pull_cli podman
    set -g regview_pull_docker false
    if test $engine = docker
        set -g regview_pull_cli docker --host unix:///run/user/(id -u)/docker.sock
        set -g regview_pull_docker true
    end

    function interrupt_regview_e2e_pull --on-signal INT --on-signal TERM
        cleanup_regview_e2e_pull
        functions --erase interrupt_regview_e2e_pull
        # Re-raise after cleanup so the caller observes an interrupted process.
        kill -TERM $fish_pid
    end

    function cleanup_regview_e2e_pull --on-event fish_exit
        set -l result 0
        $regview_pull_cli system prune --all --force; or set result 1
        set -l images ($regview_pull_cli image ls --all --quiet)
        if test $status -ne 0; or test (count $images) -ne 0
            echo 'Pull-client image store is not empty after cleanup.' >&2
            set result 1
        end
        if test $regview_pull_docker = true
            systemctl --user stop docker.service; or set result 1
        end
        if test $result -ne 0
            echo 'Pull-client cleanup failed.' >&2
        end
        return $result
    end

    # Keep the cleanup handler installed even if startup/readiness fails.
    set -l result 0
    if test $engine = docker
        systemctl --user start docker.service; or set result 1
        if test $result -eq 0
            for attempt in (seq 1 30)
                if $regview_pull_cli info >/dev/null 2>&1
                    break
                end
                sleep 1
            end
            $regview_pull_cli info >/dev/null; or set result 1
        end
    end

    # Clear leftovers from interrupted runs before checking the empty store.
    if test $result -eq 0
        $regview_pull_cli system prune --all --force; or set result 1
    end
    if test $result -eq 0
        set -l images ($regview_pull_cli image ls --all --quiet)
        if test $status -ne 0; or test (count $images) -ne 0
            echo 'Pull-client image store is not empty.' >&2
            set result 1
        end
    end
    if test $result -eq 0
        if test $engine = podman
            $regview_pull_cli pull --tls-verify=false --policy=always $image; or set result 1
        else
            $regview_pull_cli pull $image; or set result 1
        end
    end
    if test $result -eq 0
        # An explicit pull above must supply the image used by the run.
        $regview_pull_cli run --rm --pull=never $image; or set result 1
    end

    cleanup_regview_e2e_pull; or set result 1
    functions --erase cleanup_regview_e2e_pull interrupt_regview_e2e_pull
    return $result
end
