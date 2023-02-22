prefix=/usr/local
bindir=$(prefix)/bin

export BUILDX_CMD ?= docker buildx

.PHONY: binaries
binaries:
	hack/binaries

.PHONY: images
images:
# moby/buildkit:local and moby/buildkit:local-rootless are created on Docker
	hack/images local moby/buildkit
	TARGET=rootless hack/images local moby/buildkit

.PHONY: install
install:
	mkdir -p $(DESTDIR)$(bindir)
	install bin/* $(DESTDIR)$(bindir)

.PHONY: clean
clean:
	rm -rf ./bin

.PHONY: test
test:
	./hack/test integration gateway dockerfile

.PHONY: lint
lint:
	./hack/lint

.PHONY: validate-vendor
validate-vendor:
	$(BUILDX_CMD) bake validate-vendor

.PHONY: validate-shfmt
validate-shfmt:
	./hack/validate-shfmt

.PHONY: shfmt
shfmt:
	./hack/shfmt

.PHONY: validate-authors
validate-authors:
	$(BUILDX_CMD) bake validate-authors

.PHONY: validate-generated-files
validate-generated-files:
	./hack/validate-generated-files

.PHONY: validate-all
validate-all: test lint validate-vendor validate-generated-files

.PHONY: vendor
vendor:
	$(eval $@_TMP_OUT := $(shell mktemp -d -t buildkit-output.XXXXXXXXXX))
	$(BUILDX_CMD) bake --set "*.output=type=local,dest=$($@_TMP_OUT)" vendor
	rm -rf ./vendor
	cp -R "$($@_TMP_OUT)"/out/* .
	rm -rf "$($@_TMP_OUT)"/

.PHONY: generated-files
generated-files:
	./hack/update-generated-files

.PHONY: authors
authors:
	$(BUILDX_CMD) bake authors
