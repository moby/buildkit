SHELL = /bin/bash

.PHONY: test

# these tests run in isolation by calling go test -run=... or the equivalent.
ISOLATED_TESTS +=
ifdef CI
	ISOLATED_TESTS = TestControl_Isolated \
					 TestSystemDriven_Isolated \
					 TestHeapDriven_Isolated
else
	ISOLATED_TESTS = TestControl_Isolated \
					 TestSystemDriven_Isolated \
					 TestHeapDriven_Isolated \
					 TestCgroupsDriven_Create_Isolated \
					 TestCgroupsDriven_Docker_Isolated
endif

test: test-binary test-docker

test-binary:
	go test -v ./... # run all the non-isolated tests.
	# foreach does not actually execute each iteration; it expands the text, and it's executed all at once
    # that's why we use && true, to shorcircuit if a test fails.
	$(foreach name,$(ISOLATED_TESTS),TEST_ISOLATED=1 go test -v -test.run=$(name) ./... && ) true

test-docker: docker
	docker run --memory=32MiB --memory-swap=32MiB -e TEST_DOCKER_MEMLIMIT=33554432 raulk/watchdog:latest
	$(foreach name,$(ISOLATED_TESTS),docker run \
		--memory=32MiB --memory-swap=32MiB \
		-e TEST_ISOLATED=1 \
		-e TEST_DOCKER_MEMLIMIT=33554432 \
		raulk/watchdog:latest /root/watchdog.test -test.v -test.run=$(name) ./... && ) true

docker:
	docker build -f ./Dockerfile.test  -t raulk/watchdog:latest .
