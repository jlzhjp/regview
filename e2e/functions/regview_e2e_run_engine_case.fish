function regview_e2e_run_engine_case
    set -l engine $argv[1]
    set -l mode $argv[2]

    set -l image docker.io/library/hello-world:latest
    set -l local_image localhost:5000/library/hello-world:latest
    set -l docker_socket /var/run/docker.sock
    set -l docker_host unix://$docker_socket
    set -l container_cli
    set -l regview_cmd /opt/regview-e2e/bin/regview --backends $engine --hostnames localhost:5000
    set -g regview_case_log (mktemp); or return 1
    set -g regview_case_pid
    set -eg rootless_docker_started

    function cleanup_regview_e2e_case --on-event fish_exit
        if set -q regview_case_pid
            kill $regview_case_pid 2>/dev/null
            wait $regview_case_pid 2>/dev/null
            set -eg regview_case_pid
        end
        if set -q rootless_docker_started
            systemctl --user stop docker.service >/dev/null 2>&1
            set -eg rootless_docker_started
        end
        if set -q regview_case_log
            rm -f $regview_case_log
            set -eg regview_case_log
        end
    end

    if test $engine = podman
        set container_cli podman
    else
        if test $mode = rootless
            set docker_socket /run/user/(id -u)/docker.sock
            set docker_host unix://$docker_socket
            set -g rootless_docker_started true
            systemctl --user start docker.service; or return 1
        else
            systemctl enable --now docker.service; or return 1
        end

        set container_cli docker --host $docker_host
        for attempt in (seq 1 30)
            if $container_cli info >/dev/null 2>&1
                break
            end
            sleep 1
        end
        $container_cli info >/dev/null; or return 1
        if not $container_cli info --format '{{.DriverStatus}}' | string match -q '*io.containerd.snapshotter.v1*'
            echo 'Docker is not using the containerd image store.' >&2
            return 1
        end
        set -a regview_cmd --docker-engine-socket $docker_socket
    end

    $container_cli pull $image; or return 1
    $container_cli tag $image $local_image; or return 1

    $regview_cmd >$regview_case_log 2>&1 &
    set -g regview_case_pid $last_pid

    for attempt in (seq 1 30)
        if curl --silent --fail -H 'Host: localhost:5000' \
                http://127.0.0.1:5000/v2/ >/dev/null
            break
        end
        if not kill -0 $regview_case_pid 2>/dev/null
            cat $regview_case_log >&2
            return 1
        end
        sleep 1
    end

    curl --silent --show-error --fail \
        -H 'Host: localhost:5000' \
        http://127.0.0.1:5000/v2/library/hello-world/tags/list; or return 1

    if test $engine = podman
        $container_cli pull --tls-verify=false --policy=always $local_image; or return 1
    else
        $container_cli pull $local_image; or return 1
    end
    $container_cli run --rm $local_image
end
