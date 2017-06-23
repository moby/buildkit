
BINARIES=bin/buildd-standalone bin/buildd-containerd bin/buildctl	bin/buildctl-darwin

binaries: $(BINARIES)

bin/buildctl-darwin: FORCE
	mkdir -p bin
	docker build --build-arg GOOS=darwin -t buildkit:buildctl-darwin --target buildctl -f ./hack/dockerfiles/test.Dockerfile --force-rm .
	( containerID=$$(docker create buildkit:buildctl-darwin noop); \
		docker cp $$containerID:/usr/bin/buildctl $@; \
		docker rm $$containerID )
	chmod +x $@

bin/%: FORCE
	mkdir -p bin
	docker build -t buildkit:$* --target $* -f ./hack/dockerfiles/test.Dockerfile --force-rm .
	( containerID=$$(docker create buildkit:$* noop); \
		docker cp $$containerID:/usr/bin/$* $@; \
		docker rm $$containerID )
	chmod +x $@

test:
	./hack/test

lint:
	./hack/lint
	
validate-vendor:
	./hack/validate-vendor

validate-all: test lint validate-vendor

vendor:
	./hack/update-vendor
	
.PHONY: vendor test binaries lint validate-all validate-vendor
FORCE: