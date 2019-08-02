module github.com/moby/buildkit

go 1.11

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/Microsoft/go-winio v0.4.13-0.20190628181719-7cdfd71a950d
	github.com/apache/thrift v0.0.0-20161221203622-b2a4d4ae21c7 // indirect
	github.com/codahale/hdrhistogram v0.0.0-20160425231609-f8ad88b59a58 // indirect
	github.com/containerd/cgroups v0.0.0-20190717030353-c4b9ac5c7601 // indirect
	github.com/containerd/console v0.0.0-20181022165439-0650fd9eeb50
	github.com/containerd/containerd v1.3.0-beta.0.0.20190802175917-f0821348b4a8
	github.com/containerd/continuity v0.0.0-20190426062206-aaeac12a7ffc
	github.com/containerd/fifo v0.0.0-20180307165137-3d5202aec260 // indirect
	github.com/containerd/go-cni v0.0.0-20190610170741-5a4663dad645
	github.com/containerd/go-runc v0.0.0-20190603165425-9007c2405372
	github.com/containerd/ttrpc v0.0.0-20190613183316-1fb3814edf44 // indirect
	github.com/containerd/typeurl v0.0.0-20180627222232-a93fcdb778cd // indirect
	github.com/containernetworking/cni v0.6.1-0.20180218032124-142cde0c766c // indirect
	github.com/coreos/go-systemd v0.0.0-20190620071333-e64a0ec8b42a
	github.com/docker/cli v0.0.0-20190321234815-f40f9c240ab0
	github.com/docker/distribution v2.7.1-0.20190205005809-0d3efadf0154+incompatible
	github.com/docker/docker v1.14.0-0.20190319215453-e7b5f7dbe98c
	github.com/docker/docker-credential-helpers v0.6.0 // indirect
	github.com/docker/go-connections v0.3.0
	github.com/docker/go-events v0.0.0-20170721190031-9461782956ad // indirect
	github.com/docker/libnetwork v0.8.0-dev.2.0.20190604151032-3c26b4e7495e
	github.com/fullsailor/pkcs7 v0.0.0-20190404230743-d7302db945fa // indirect
	github.com/godbus/dbus v0.0.0-20181101234600-2ff6f7ffd60f // indirect
	github.com/gofrs/flock v0.7.0
	github.com/gogo/googleapis v1.1.0
	github.com/gogo/protobuf v1.2.0
	github.com/golang/protobuf v1.2.0
	github.com/google/go-cmp v0.3.0
	github.com/google/shlex v0.0.0-20150127133951-6f45313302b9
	github.com/grpc-ecosystem/grpc-opentracing v0.0.0-20180507213350-8e809c8a8645
	github.com/hashicorp/go-immutable-radix v1.0.0
	github.com/hashicorp/golang-lru v0.0.0-20160207214719-a0d98a5f2880
	github.com/hashicorp/uuid v0.0.0-20160311170451-ebb0a03e909c // indirect
	github.com/ishidawataru/sctp v0.0.0-20180213033435-07191f837fed // indirect
	github.com/jaguilar/vt100 v0.0.0-20150826170717-2703a27b14ea
	github.com/kr/pretty v0.1.0 // indirect
	github.com/miscreant/miscreant-go v0.0.0-20190615163012-4f5dc8c406f6 // indirect
	github.com/miscreant/miscreant.go v0.0.0-20190615163012-4f5dc8c406f6 // indirect
	github.com/mitchellh/hashstructure v0.0.0-20170609045927-2bca23e0e452
	github.com/morikuni/aec v0.0.0-20170113033406-39771216ff4c
	github.com/opencontainers/go-digest v1.0.0-rc1
	github.com/opencontainers/image-spec v1.0.1
	github.com/opencontainers/runc v1.0.0-rc8.0.20190621203724-f4982d86f7fd
	github.com/opencontainers/runtime-spec v0.0.0-20180909173843-eba862dc2470
	github.com/opentracing-contrib/go-stdlib v0.0.0-20171029140428-b1a47cfbdd75
	github.com/opentracing/opentracing-go v0.0.0-20171003133519-1361b9cd60be
	github.com/pkg/errors v0.8.1
	github.com/pkg/profile v1.2.1
	github.com/prometheus/procfs v0.0.3 // indirect
	github.com/serialx/hashring v0.0.0-20190422032157-8b2912629002
	github.com/sirupsen/logrus v1.4.1
	github.com/stretchr/testify v1.3.0
	github.com/syndtr/gocapability v0.0.0-20180916011248-d98352740cb2 // indirect
	github.com/tonistiigi/fsutil v0.0.0-20190327153851-3bbb99cdbd76
	github.com/tonistiigi/units v0.0.0-20180711220420-6950e57a87ea
	github.com/uber/jaeger-client-go v0.0.0-20180103221425-e02c85f9069e
	github.com/uber/jaeger-lib v1.2.1 // indirect
	github.com/urfave/cli v0.0.0-20171014202726-7bc6a0acffa5
	github.com/vishvananda/netlink v1.0.0 // indirect
	github.com/vishvananda/netns v0.0.0-20180720170159-13995c7128cc // indirect
	go.etcd.io/bbolt v1.3.3-0.20190528202153-2eb7227adea1
	golang.org/x/crypto v0.0.0-20190308221718-c2843e01d9a2
	golang.org/x/net v0.0.0-20190522155817-f3200d17e092
	golang.org/x/sync v0.0.0-20181221193216-37e7f081c4d4
	golang.org/x/sys v0.0.0-20190602015325-4c4f7f33c9ed
	golang.org/x/time v0.0.0-20161028155119-f51c12702a4d
	google.golang.org/genproto v0.0.0-20180817151627-c66870c02cf8
	google.golang.org/grpc v1.20.1
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
	gopkg.in/square/go-jose.v2 v2.3.1 // indirect
	gotest.tools v2.2.0+incompatible
)

replace github.com/hashicorp/go-immutable-radix => github.com/tonistiigi/go-immutable-radix v0.0.0-20170803185627-826af9ccf0fe

replace github.com/jaguilar/vt100 => github.com/tonistiigi/vt100 v0.0.0-20190402012908-ad4c4a574305
