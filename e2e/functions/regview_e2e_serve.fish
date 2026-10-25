function regview_e2e_serve
    set -l engine $argv[1]
    set -l mode $argv[2]

    set -l image docker.io/library/hello-world:latest
    set -l registry $argv[3]
    set -l local_image $registry/library/hello-world:latest
    set -l docker_socket /var/run/docker.sock
    set -l docker_host unix://$docker_socket
    set -l container_cli
    set -l regview_cmd /opt/regview-e2e/bin/regview --backends $engine \
        --registry-listen 0.0.0.0:5000 --hostnames $registry

    if test $engine = podman
        set container_cli podman
    else
        if test $mode = rootless
            set docker_socket /run/user/(id -u)/docker.sock
            set docker_host unix://$docker_socket
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

    exec $regview_cmd
end
