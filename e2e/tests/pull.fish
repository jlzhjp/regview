#!/usr/bin/env fish
# Exercise cold pulls and failure cleanup without touching real container stores.
source (path dirname (status filename))/../functions/regview_e2e_pull.fish

function id
    switch $argv[1]
        case -un
            echo e2e-pull
        case -u
            echo 1234
        case '*'
            return 1
    end
end

function systemctl
    switch (string join ' ' -- $argv)
        case '--user start docker.service'
            test $scenario != start-error
        case '--user stop docker.service'
            set -g stopped true
            test $prunes -ge 1; or set -g violation true
            test $scenario != stop-error
        case '*'
            set -g violation true
            return 1
    end
end

function docker
    if test "$argv[1..2]" != '--host unix:///run/user/1234/docker.sock'
        set -g violation true
        return 1
    end
    fake_engine $argv[3..]
end

function podman
    fake_engine $argv
end

function fake_engine
    switch (string join ' ' -- $argv)
        case info
            return 0
        case 'system prune --all --force'
            set -g prunes (math $prunes + 1)
            if test $scenario = pre-prune-error; and test $prunes -eq 1
                return 1
            end
            if test $scenario = post-prune-error; and test $prunes -eq 2
                return 1
            end
            if test $scenario != dirty-store
                set -g stored false
            end
            if test $scenario = signal; and test $prunes -eq 2
                echo cleaned >$signal_marker
            end
        case 'image ls --all --quiet'
            test $scenario != list-error; or return 1
            if test $stored = true
                echo cached-image
            end
        case 'pull *'
            set -g pulled true
            test $stored = false; or set -g violation true
            test $scenario != pull-error; or return 1
            if test $scenario = signal
                kill -$test_signal $fish_pid
                return 1
            end
            set -g stored true
        case 'run --rm --pull=never *'
            set -g ran true
            test $pulled = true; and test $stored = true; or set -g violation true
            test $scenario != run-error; or return 1
        case '*'
            set -g violation true
            return 1
    end
    return 0
end

# A subprocess is needed to verify that interruption both cleans and fails.
if test (count $argv) -eq 2
    set -g scenario signal
    set -g test_signal $argv[1]
    set -g signal_marker $argv[2]
    set -g prunes 0
    set -g stored true
    set -g pulled false
    regview_e2e_pull podman 192.0.2.1:5000/library/hello-world:latest
    exit $status
end

for engine in docker podman
    for test_scenario in success pull-error run-error pre-prune-error post-prune-error dirty-store list-error start-error stop-error
        set -g scenario $test_scenario
        if test $engine = podman; and contains -- $scenario start-error stop-error
            continue
        end
        set -g prunes 0
        set -g stored true
        set -g pulled false
        set -g ran false
        set -g stopped false
        set -g violation false
        regview_e2e_pull $engine 192.0.2.1:5000/library/hello-world:latest >/dev/null 2>&1
        set -l result $status
        set -l failed false
        if test $scenario = success
            test $result -eq 0; and test $ran = true; and test $stored = false; or set failed true
        else
            test $result -ne 0; or set failed true
        end
        if test $scenario = pull-error
            test $ran = false; or set failed true
        end
        if contains -- $scenario pre-prune-error dirty-store list-error start-error
            test $pulled = false; or set failed true
        end
        if test $scenario != start-error
            test $prunes -eq 2; or set failed true
        end
        if test $engine = docker
            test $stopped = true; or set failed true
        end
        if test $failed = true; or test $violation = true
            echo "FAIL: $engine $scenario (status=$result, prunes=$prunes, pulled=$pulled, ran=$ran, stored=$stored, stopped=$stopped)" >&2
            exit 1
        end
        echo "PASS: $engine $scenario"
    end
end

for test_signal in INT TERM
    set -l marker (mktemp); or exit 1
    fish (status filename) $test_signal $marker >/dev/null 2>&1
    set -l result $status
    set -l cleaned (cat $marker)
    rm -f $marker
    if test $result -eq 0; or test "$cleaned" != cleaned
        echo "FAIL: $test_signal must clean up and fail the subprocess" >&2
        exit 1
    end
    echo "PASS: $test_signal cleanup"
end
