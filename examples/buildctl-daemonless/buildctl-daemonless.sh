#!/bin/sh
# buildctl-daemonless.sh spawns ephemeral buildkitd for executing buildctl.
#
# Usage: buildctl-daemonless.sh build ...
#
# Flags for buildkitd can be specified as $BUILDKITD_FLAGS .
#
# The script is compatible with BusyBox shell.
set -eu

: ${BUILDCTL=buildctl}
: ${BUILDCTL_CONNECT_RETRIES_MAX=10}
: ${BUILDKITD=buildkitd}
: ${BUILDKITD_FLAGS=}
: ${ROOTLESSKIT=rootlesskit}

# $tmp holds the following files:
# * pid - pid of buildkitd or rootlesskit
# * addr - address where you can connect to buildkitd
# * log - stdout+stderr of buildkitd
# * log_tail_pid - tail -f on log file so that stdout is preserved
tmp=$(mktemp -d /tmp/buildctl-daemonless.XXXXXX)
echo "Runtime dir: $tmp"

trap_cleanup() {
	# cleanup tail on log file
    log_tail_pid=$(cat "$tmp/log_tail_pid" 2>/dev/null || true)

    if [ -n "$log_tail_pid" ] && kill -0 "$log_tail_pid" 2>/dev/null; then
		kill -s TERM "$log_tail_pid" 2>/dev/null || true
	fi

    pid=$(cat "$tmp/pid" 2>/dev/null || true)
	# -n - string is non-zero
	# kill -0 - test if the process exists and we can send signal to it
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        echo "Stopping buildkitd (pid=$pid)..."
        kill -s TERM "$pid" 2>/dev/null || true

        # Wait up to 5 seconds for clean exit
        for i in $(seq 1 5); do
            if ! kill -0 "$pid" 2>/dev/null; then
                echo "buildkitd exited"
                break
            fi
            sleep 1
        done

        # Force kill if still running
        if kill -0 "$pid" 2>/dev/null; then
            echo "buildkitd did not exit in time, forcibly killing..."
            kill -9 "$pid" 2>/dev/null || true
        fi
    fi

	echo "cleaning up runtime dir $tmp"
    rm -rf "$tmp"
}
trap trap_cleanup EXIT

DEBUG_FLAGS=""

if [ ! -z "${DEBUG+x}" ]; then
  DEBUG_FLAGS="--debug"
fi

startBuildkitd() {
    addr=
    helper=
    if [ $(id -u) = 0 ]; then
        addr=unix:///run/buildkit/buildkitd.sock
        echo "Running as root"
    else
        addr=unix://$XDG_RUNTIME_DIR/buildkit/buildkitd.sock
        helper=$ROOTLESSKIT
        echo "Running as non-root"
    fi

	$helper "$BUILDKITD" $BUILDKITD_FLAGS $DEBUG_FLAGS --addr="$addr" >>"$tmp/log" 2>&1 &
	# gets pid of helper or buildkitd
    pid=$!

	# pipe buildkitd logs to stdout
	tail -F "$tmp/log" &
	log_tail_pid=$!
	
	echo $log_tail_pid >$tmp/log_tail_pid
    echo $pid >$tmp/pid
    echo $addr >$tmp/addr
    echo "Started buildkitd with pid $pid and addr $addr"
}

# buildkitd supports NOTIFY_SOCKET but as far as we know, there is no easy way
# to wait for NOTIFY_SOCKET activation using busybox-builtin commands...
waitForBuildkitd() {
    addr=$(cat $tmp/addr)
    try=0
    max=$BUILDCTL_CONNECT_RETRIES_MAX
    until $BUILDCTL --addr=$addr debug workers >/dev/null 2>&1; do
        if [ $try -gt $max ]; then
            echo >&2 "could not connect to $addr after $max trials"
            echo >&2 "========== log =========="
            cat >&2 $tmp/log
            exit 1
        fi
        sleep $(awk "BEGIN{print (100 + $try * 20) * 0.001}")
        try=$(expr $try + 1)
    done
}

startBuildkitd
waitForBuildkitd
$BUILDCTL $DEBUG_FLAGS --addr=$(cat $tmp/addr) "$@"
