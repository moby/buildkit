# Go memory watchdog

> 🐺 A library to curb OOMs by running Go GC according to a user-defined policy.

[![godocs](https://img.shields.io/badge/godoc-reference-5272B4.svg?style=flat-square)](https://godoc.org/github.com/raulk/go-watchdog)
[![build status](https://circleci.com/gh/raulk/go-watchdog.svg?style=svg)](https://circleci.com/gh/raulk/go-watchdog)

Package watchdog runs a singleton memory watchdog in the process, which
watches memory utilization and forces Go GC in accordance with a
user-defined policy.

There three kinds of watchdogs:

1. heap-driven (`watchdog.HeapDriven()`): applies a heap limit, adjusting GOGC
   dynamically in accordance with the policy.
2. system-driven (`watchdog.SystemDriven()`): applies a limit to the total
   system memory used, obtaining the current usage through elastic/go-sigar.
3. cgroups-driven (`watchdog.CgroupDriven()`): discovers the memory limit from
   the cgroup of the process (derived from /proc/self/cgroup), or from the
   root cgroup path if the PID == 1 (which indicates that the process is
   running in a container). It uses the cgroup stats to obtain the
   current usage.

The watchdog's behaviour is controlled by the policy, a pluggable function
that determines when to trigger GC based on the current utilization. This
library ships with two policies:

1. watermarks policy (`watchdog.NewWatermarkPolicy()`): runs GC at configured
   watermarks of memory utilisation.
2. adaptive policy (`watchdog.NewAdaptivePolicy()`): runs GC when the current
   usage surpasses a dynamically-set threshold.

You can easily write a custom policy tailored to the allocation patterns of
your program.

## Recommended way to set up the watchdog

The recommended way to set up the watchdog is as follows, in descending order
of precedence. This logic assumes that the library supports setting a heap
limit through an environment variable (e.g. MYAPP_HEAP_MAX) or config key.

1. If heap limit is set and legal, initialize a heap-driven watchdog.
2. Otherwise, try to use the cgroup-driven watchdog. If it succeeds, return.
3. Otherwise, try to initialize a system-driven watchdog. If it succeeds, return.
4. Watchdog initialization failed. Log a warning to inform the user that
   they're flying solo.

## Running the tests

Given the low-level nature of this component, some tests need to run in
isolation, so that they don't carry over Go runtime metrics. For completeness,
this module uses a Docker image for testing, so we can simulate cgroup memory
limits.

The test execution and docker builds have been conveniently packaged in a
Makefile. Run with:

```shell
$ make
```

## Why is this even needed?

The garbage collector that ships with the go runtime is pretty good in some
regards (low-latency, negligible no stop-the-world), but it's insatisfactory in
a number of situations that yield ill-fated outcomes:

1. it is incapable of dealing with bursty/spiky allocations efficiently;
   depending on the workload, the program may OOM as a consequence of not
   scheduling GC in a timely manner.
2. part of the above is due to the fact that go doesn't concern itself with any
   limits. To date, it is not possible to set a maximum heap size. 
2. its default policy of scheduling GC when the heap doubles, coupled with its
   ignorance of system or process limits, can easily cause it to OOM.

For more information, check out these GitHub issues:

* https://github.com/golang/go/issues/42805
* https://github.com/golang/go/issues/42430
* https://github.com/golang/go/issues/14735
* https://github.com/golang/go/issues/16843
* https://github.com/golang/go/issues/10064
* https://github.com/golang/go/issues/9849

## License

Dual-licensed: [MIT](./LICENSE-MIT), [Apache Software License v2](./LICENSE-APACHE), by way of the
[Permissive License Stack](https://protocol.ai/blog/announcing-the-permissive-license-stack/).
