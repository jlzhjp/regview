function prepare_e2e_environment
    if test (id -u) -ne 0
        echo 'Run the e2e environment as root.' >&2
        return 1
    end

    systemctl enable --now systemd-networkd.service; or return 1
    systemctl start dbus-broker.service; or return 1
    modprobe iptable_nat; or return 1

    set -l connected false
    for attempt in (seq 1 30)
        if curl --silent --connect-timeout 2 --max-time 4 \
                --output /dev/null https://registry-1.docker.io/v2/
            set connected true
            break
        end
        sleep 1
    end
    if test $connected != true
        echo 'Guest network did not reach Docker Hub.' >&2
        return 1
    end

    # A non-loopback address also works from Docker's rootless network namespace.
    set -l addresses (ip -4 -o address show scope global | string match --regex --groups-only '\sinet ([0-9.]+)/')
    if test (count $addresses) -eq 0
        echo 'Guest has no global IPv4 address.' >&2
        return 1
    end
    set -g regview_e2e_registry "$addresses[1]:5000"

    # useradd allocates non-overlapping subordinate ranges for the two users.
    for account in e2e e2e-pull
        id $account >/dev/null 2>&1; or useradd --create-home --shell /usr/bin/fish $account; or return 1
        if not grep -q "^$account:" /etc/subuid; or not grep -q "^$account:" /etc/subgid
            echo "Missing subordinate UID/GID ranges for $account." >&2
            return 1
        end
    end

    for account in e2e e2e-pull
        set -l config_dir /home/$account/.config/docker
        install -d -m 0700 -o $account -g $account /home/$account/.config $config_dir; or return 1
        printf '{"features":{"containerd-snapshotter":true},"insecure-registries":["%s"]}\n' \
            $regview_e2e_registry >$config_dir/daemon.json; or return 1
        chown $account:$account $config_dir/daemon.json; or return 1
        chmod 0600 $config_dir/daemon.json; or return 1
        run0 --pipe --user=$account --chdir=/home/$account dockerd-rootless-setuptool.sh install --force; or return 1
        run0 --pipe --user=$account --chdir=/home/$account systemctl --user disable --now docker.service; or return 1
    end

    echo "Guest networking is ready; registry endpoint is $regview_e2e_registry."
end
