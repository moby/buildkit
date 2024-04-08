startBuildkitd() {
    addr=
    helper=
    if [ $(id -u) = 0 ]; then
        addr=unix:///run/buildkit/buildkitd.sock
    else
        addr=unix://$XDG_RUNTIME_DIR/buildkit/buildkitd.sock
        helper=$ROOTLESSKIT
    fi
    $helper $BUILDKITD $BUILDKITD_FLAGS --addr=$addr >$tmp/log 2>&1 &
    pid=$!
    echo $pid >$tmp/pid
    echo $addr >$tmp/addr
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

startRegistry() {
    docker run -d -p 80:5000 --name registry-http registry:2
    docker run -d \
    --restart=always \
    --name registry \
    -v "$(pwd)"/certs:/certs \
    -e REGISTRY_HTTP_ADDR=0.0.0.0:443 \
    -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/domain.crt \
    -e REGISTRY_HTTP_TLS_KEY=/certs/domain.key \
    -p 4443:443 \
    registry:2
    skopeo copy --src-tls-verify=false --dest-tls-verify=false  docker://registry.alauda.cn:60080/ops/alpine:3 docker://localhost/ops/alpine:3  --all
    skopeo copy --src-tls-verify=false --dest-tls-verify=false  docker://registry.alauda.cn:60080/ops/alpine:3 docker://localhost:4443/ops/alpine:3  --all 
}

config_dns() {
    echo "127.0.0.1 test.registry.com" >> /etc/hosts
}

perpare() {
    startRegistry
    config_dns
}
