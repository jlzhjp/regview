function prepare_e2e_environment
    if test (id -u) -ne 0
        echo 'Run the e2e environment as root.' >&2
        return 1
    end

    systemctl enable --now systemd-networkd.service; or return 1
    systemctl start dbus-broker.service; or return 1

    id e2e >/dev/null 2>&1; or useradd --create-home --shell /usr/bin/fish e2e; or return 1

    if not grep -q '^e2e:' /etc/subuid 2>/dev/null
        usermod --add-subuids 200000-265535 e2e; or return 1
    end
    if not grep -q '^e2e:' /etc/subgid 2>/dev/null
        usermod --add-subgids 200000-265535 e2e; or return 1
    end

    modprobe iptable_nat; or return 1

    install -d -m 0700 -o e2e -g e2e /home/e2e/.config; or return 1
    install -d -m 0700 -o e2e -g e2e /home/e2e/.config/docker; or return 1
    install -m 0600 -o e2e -g e2e /etc/docker/daemon.json \
        /home/e2e/.config/docker/daemon.json; or return 1
    run0 --user=e2e --chdir=/home/e2e dockerd-rootless-setuptool.sh install --force; or return 1
    run0 --user=e2e --chdir=/home/e2e systemctl --user disable --now docker.service; or return 1

    for attempt in (seq 1 30)
        if curl --silent --connect-timeout 2 --max-time 4 \
                --output /dev/null https://registry-1.docker.io/v2/
            echo 'Guest networking and Docker Hub connectivity are ready.'
            return 0
        end
        sleep 1
    end

    echo 'Guest network did not reach Docker Hub.' >&2
    return 1
end
