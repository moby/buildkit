test:
	docker build -f ./hack/dockerfiles/test.Dockerfile .

vendor:
	./hack/update-vendor
	
.PHONY: vendor test