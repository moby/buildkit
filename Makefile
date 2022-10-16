ifneq (, $(BUILDX_BIN))
	export BUILDX_CMD = $(BUILDX_BIN)
else ifneq (, $(shell docker buildx version))
	export BUILDX_CMD = docker buildx
else ifneq (, $(shell which buildx))
	export BUILDX_CMD = $(which buildx)
else
	export BUILDX_CMD = docker buildx
endif

prefix=/usr/local
bindir=$(prefix)/bin

binaries: FORCE
	hack/binaries

images: FORCE
# moby/buildkit:local and moby/buildkit:local-rootless are created on Docker
	hack/images local moby/buildkit
	TARGET=rootless hack/images local moby/buildkit

install: FORCE
	mkdir -p $(DESTDIR)$(bindir)
	install bin/* $(DESTDIR)$(bindir)

clean: FORCE
	rm -rf ./bin

test:
	./hack/test integration gateway dockerfile

lint:
	./hack/lint

validate-vendor:
	$(BUILDX_CMD) bake validate-vendor

validate-shfmt:
	./hack/validate-shfmt

shfmt:
	./hack/shfmt

validate-generated-files:
	./hack/validate-generated-files

validate-all: test lint validate-vendor validate-generated-files

vendor:
	./hack/update-vendor

generated-files:
	./hack/update-generated-files

.PHONY: vendor generated-files test binaries images install clean lint validate-all validate-vendor validate-generated-files
FORCE:
