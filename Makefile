test:
	./hack/test

vendor:
	./hack/update-vendor
	
.PHONY: vendor test