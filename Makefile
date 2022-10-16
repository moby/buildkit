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

.PHONY: binaries
binaries:
	$(BUILDX_CMD) bake binaries

.PHONY: cross
cross:
	$(BUILDX_CMD) bake binaries-cross

.PHONY: release
release:
	$(BUILDX_CMD) bake release
	mv -f $(CURDIR)/bin/release/**/* $(CURDIR)/bin/release/
	find $(CURDIR)/bin/release -type d -empty -delete

.PHONY: images
images:
# moby/buildkit:local and moby/buildkit:local-rootless are created on Docker
	$(BUILDX_CMD) bake image
	IMAGE_TARGET=rootless $(BUILDX_CMD) bake image

.PHONY: install
install:
	mkdir -p $(DESTDIR)$(bindir)
	install bin/build/* $(DESTDIR)$(bindir)

.PHONY: clean
clean:
	rm -rf ./bin

.PHONY: test
test:
	hack/test integration gateway dockerfile

.PHONY: lint
lint:
	$(BUILDX_CMD) bake lint

.PHONY: validate-vendor
validate-vendor:
	$(BUILDX_CMD) bake validate-vendor

.PHONY: validate-authors
validate-authors:
	$(BUILDX_CMD) bake validate-authors

.PHONY: validate-generated-files
validate-generated-files:
	$(BUILDX_CMD) bake validate-generated-files

.PHONY: validate-shfmt
validate-shfmt:
	$(BUILDX_CMD) bake validate-shfmt

.PHONY: validate-all
validate-all: test lint validate-vendor validate-generated-files validate-shfmt

.PHONY: vendor
vendor:
	hack/update-vendor

.PHONY: authors
authors:
	$(BUILDX_CMD) bake authors

.PHONY: generated-files
generated-files:
	$(BUILDX_CMD) bake generated-files

.PHONY: shfmt
shfmt:
	$(BUILDX_CMD) bake shfmt
