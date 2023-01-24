package watchdog

import (
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/elastic/gosigar"
	"github.com/benbjohnson/clock"
)

// ErrNotSupported is returned when the watchdog does not support the requested
// run mode in the current OS/arch.
var ErrNotSupported = errors.New("watchdog run mode not supported")

// PolicyTempDisabled is a marker value for policies to signal that the policy
// is temporarily disabled. Use it when all hope is lost to turn around from
// significant memory pressure (such as when above an "extreme" watermark).
const PolicyTempDisabled uint64 = math.MaxUint64

// The watchdog is designed to be used as a singleton; global vars are OK for
// that reason.
var (
	// Logger is the logger to use. If nil, it will default to a logger that
	// proxies to a standard logger using the "[watchdog]" prefix.
	Logger logger = &stdlog{log: log.New(log.Writer(), "[watchdog] ", log.LstdFlags|log.Lmsgprefix)}

	// Clock can be used to inject a mock clock for testing.
	Clock = clock.New()

	// ForcedGCFunc specifies the function to call when forced GC is necessary.
	// Its default value is runtime.GC, but it can be set to debug.FreeOSMemory
	// to force the release of memory to the OS.
	ForcedGCFunc = runtime.GC

	// NotifyGC, if non-nil, will be called when a GC has happened.
	// Deprecated: use RegisterPostGCNotifee instead.
	NotifyGC func() = func() {}

	// HeapProfileThreshold sets the utilization threshold that will trigger a
	// heap profile to be taken automatically. A zero value disables this feature.
	// By default, it is disabled.
	HeapProfileThreshold float64

	// HeapProfileMaxCaptures sets the maximum amount of heap profiles a process will generate.
	// This limits the amount of episodes that will be captured, in case the
	// utilization climbs repeatedly over the threshold. By default, it is 10.
	HeapProfileMaxCaptures = uint(10)

	// HeapProfileDir is the directory where the watchdog will write the heap profile.
	// It will be created if it doesn't exist upon initialization. An error when
	// creating the dir will not prevent heapdog initialization; it will just
	// disable the heap profile capture feature. If zero-valued, the feature is
	// disabled.
	//
	// HeapProfiles will be written to path <HeapProfileDir>/<RFC3339Nano formatted timestamp>.heap.
	HeapProfileDir string
)

var (
	// ReadMemStats stops the world. But as of go1.9, it should only
	// take ~25µs to complete.
	//
	// Before go1.15, calls to ReadMemStats during an ongoing GC would
	// block due to the worldsema lock. As of go1.15, this was optimized
	// and the runtime holds on to worldsema less during GC (only during
	// sweep termination and mark termination).
	//
	// For users using go1.14 and earlier, if this call happens during
	// GC, it will just block for longer until serviced, but it will not
	// take longer in itself. No harm done.
	//
	// Actual benchmarks
	// -----------------
	//
	// In Go 1.15.5, ReadMem with no ongoing GC takes ~27µs in a MBP 16
	// i9 busy with another million things. During GC, it takes an
	// average of less than 175µs per op.
	//
	// goos: darwin
	// goarch: amd64
	// pkg: github.com/filecoin-project/lotus/api
	// BenchmarkReadMemStats-16                	   44530	     27523 ns/op
	// BenchmarkReadMemStats-16                	   43743	     26879 ns/op
	// BenchmarkReadMemStats-16                	   45627	     26791 ns/op
	// BenchmarkReadMemStats-16                	   44538	     26219 ns/op
	// BenchmarkReadMemStats-16                	   44958	     26757 ns/op
	// BenchmarkReadMemStatsWithGCContention-16    	      10	    183733 p50-ns	    211859 p90-ns	    211859 p99-ns
	// BenchmarkReadMemStatsWithGCContention-16    	       7	    198765 p50-ns	    314873 p90-ns	    314873 p99-ns
	// BenchmarkReadMemStatsWithGCContention-16    	      10	    195151 p50-ns	    311408 p90-ns	    311408 p99-ns
	// BenchmarkReadMemStatsWithGCContention-16    	      10	    217279 p50-ns	    295308 p90-ns	    295308 p99-ns
	// BenchmarkReadMemStatsWithGCContention-16    	      10	    167054 p50-ns	    327072 p90-ns	    327072 p99-ns
	// PASS
	//
	// See: https://github.com/golang/go/issues/19812
	// See: https://github.com/prometheus/client_golang/issues/403
	memstatsFn = runtime.ReadMemStats
	sysmemFn   = (*gosigar.Mem).Get
)

type notifeeEntry struct {
	id int
	f  func()
}

var (
	// ErrAlreadyStarted is returned when the user tries to start the watchdog more than once.
	ErrAlreadyStarted = fmt.Errorf("singleton memory watchdog was already started")
)

const (
	// stateUnstarted represents an unstarted state.
	stateUnstarted int32 = iota
	// stateRunning represents an operational state.
	stateRunning
)

// _watchdog is a global singleton watchdog.
var _watchdog struct {
	lk    sync.Mutex
	state int32

	scope UtilizationType

	hpleft uint // tracks the amount of heap profiles left.
	hpcurr bool // tracks whether a heap profile has already been taken for this episode.

	closing chan struct{}
	wg      sync.WaitGroup
}

// UtilizationType is the utilization metric in use.
type UtilizationType int

const (
	// UtilizationSystem specifies that the policy compares against actual used
	// system memory.
	UtilizationSystem UtilizationType = iota
	// UtilizationProcess specifies that the watchdog is using process limits.
	UtilizationProcess
	// UtilizationHeap specifies that the policy compares against heap used.
	UtilizationHeap
)

// PolicyCtor is a policy constructor.
type PolicyCtor func(limit uint64) (Policy, error)

// Policy is polled by the watchdog to determine the next utilisation at which
// a GC should be forced.
type Policy interface {
	// Evaluate determines when the next GC should take place. It receives the
	// current usage, and it returns the next usage at which to trigger GC.
	Evaluate(scope UtilizationType, used uint64) (next uint64)
}

// HeapDriven starts a singleton heap-driven watchdog, which adjusts GOGC
// dynamically after every GC, to honour the policy requirements.
//
// Providing a zero-valued limit will error. A minimum GOGC value is required,
// so as to avoid overscheduling GC, and overfitting to a specific target.
func HeapDriven(limit uint64, minGOGC int, policyCtor PolicyCtor) (err error, stopFn func()) {
	if limit == 0 {
		return fmt.Errorf("cannot use zero limit for heap-driven watchdog"), nil
	}

	policy, err := policyCtor(limit)
	if err != nil {
		return fmt.Errorf("failed to construct policy with limit %d: %w", limit, err), nil
	}

	if err := start(UtilizationHeap); err != nil {
		return err, nil
	}

	gcTriggered := make(chan struct{}, 16)
	setupGCSentinel(gcTriggered)

	_watchdog.wg.Add(1)
	go func() {
		defer _watchdog.wg.Done()
		defer wdrecover() // recover from panics.

		// get the initial effective GOGC; guess it's 100 (default), and restore
		// it to whatever it actually was. This works because SetGCPercent
		// returns the previous value.
		originalGOGC := debug.SetGCPercent(100)
		debug.SetGCPercent(originalGOGC)
		currGOGC := originalGOGC

		var memstats runtime.MemStats
		for {
			select {
			case <-gcTriggered:
				notifyGC()

			case <-_watchdog.closing:
				return
			}

			// recompute the next trigger.
			memstatsFn(&memstats)

			maybeCaptureHeapProfile(memstats.HeapAlloc, limit)

			// heapMarked is the amount of heap that was marked as live by GC.
			// it is inferred from our current GOGC and the new target picked.
			//
			// this accurately represents
			heapMarked := uint64(float64(memstats.NextGC) / (1 + float64(currGOGC)/100))
			if heapMarked == 0 {
				// this shouldn't happen, but just in case; avoiding a div by 0.
				Logger.Warnf("heap-driven watchdog: inferred zero heap marked; skipping evaluation")
				continue
			}

			// evaluate the policy.
			next := policy.Evaluate(UtilizationHeap, memstats.HeapAlloc)

			// calculate how much to set GOGC to honour the next trigger point.
			// next=PolicyTempDisabled value would make currGOGC extremely high,
			// greater than originalGOGC, and therefore we'd restore originalGOGC.
			currGOGC = int(((float64(next) / float64(heapMarked)) - float64(1)) * 100)
			if currGOGC >= originalGOGC {
				Logger.Debugf("heap watchdog: requested GOGC percent higher than default; capping at default; requested: %d; default: %d", currGOGC, originalGOGC)
				currGOGC = originalGOGC
			} else {
				if currGOGC < minGOGC {
					currGOGC = minGOGC // cap GOGC to avoid overscheduling.
				}
				Logger.Debugf("heap watchdog: setting GOGC percent: %d", currGOGC)
			}

			debug.SetGCPercent(currGOGC)

			memstatsFn(&memstats)
			Logger.Infof("gc finished; heap watchdog stats: heap_alloc: %d, heap_marked: %d, next_gc: %d, policy_next_gc: %d, gogc: %d",
				memstats.HeapAlloc, heapMarked, memstats.NextGC, next, currGOGC)
		}
	}()

	return nil, stop
}

// SystemDriven starts a singleton system-driven watchdog.
//
// The system-driven watchdog keeps a threshold, above which GC will be forced.
// The watchdog polls the system utilization at the specified frequency. When
// the actual utilization exceeds the threshold, a GC is forced.
//
// This threshold is calculated by querying the policy every time that GC runs,
// either triggered by the runtime, or forced by us.
func SystemDriven(limit uint64, frequency time.Duration, policyCtor PolicyCtor) (err error, stopFn func()) {
	if limit == 0 {
		var sysmem gosigar.Mem
		if err := sysmemFn(&sysmem); err != nil {
			return fmt.Errorf("failed to get system memory stats: %w", err), nil
		}
		limit = sysmem.Total
	}

	policy, err := policyCtor(limit)
	if err != nil {
		return fmt.Errorf("failed to construct policy with limit %d: %w", limit, err), nil
	}

	if err := start(UtilizationSystem); err != nil {
		return err, nil
	}

	_watchdog.wg.Add(1)
	var sysmem gosigar.Mem
	go pollingWatchdog(policy, frequency, limit, func() (uint64, error) {
		if err := sysmemFn(&sysmem); err != nil {
			return 0, err
		}
		return sysmem.ActualUsed, nil
	})

	return nil, stop
}

// pollingWatchdog starts a polling watchdog with the provided policy, using
// the supplied polling frequency. On every tick, it calls usageFn and, if the
// usage is greater or equal to the threshold at the time, it forces GC.
// usageFn is guaranteed to be called serially, so no locking should be
// necessary.
func pollingWatchdog(policy Policy, frequency time.Duration, limit uint64, usageFn func() (uint64, error)) {
	defer _watchdog.wg.Done()
	defer wdrecover() // recover from panics.

	gcTriggered := make(chan struct{}, 16)
	setupGCSentinel(gcTriggered)

	var (
		memstats  runtime.MemStats
		threshold uint64
	)

	renewThreshold := func() {
		// get the current usage.
		usage, err := usageFn()
		if err != nil {
			Logger.Warnf("failed to obtain memory utilization stats; err: %s", err)
			return
		}
		// calculate the threshold.
		threshold = policy.Evaluate(_watchdog.scope, usage)
	}

	// initialize the threshold.
	renewThreshold()

	// initialize an empty timer.
	timer := Clock.Timer(0)
	stopTimer := func() {
		if !timer.Stop() {
			<-timer.C
		}
	}

	for {
		timer.Reset(frequency)

		select {
		case <-timer.C:
			// get the current usage.
			usage, err := usageFn()
			if err != nil {
				Logger.Warnf("failed to obtain memory utilizationstats; err: %s", err)
				continue
			}

			// evaluate if a heap profile needs to be captured.
			maybeCaptureHeapProfile(usage, limit)

			if usage < threshold {
				// nothing to do.
				continue
			}
			// trigger GC; this will emit a gcTriggered event which we'll
			// consume next to readjust the threshold.
			Logger.Warnf("system-driven watchdog triggering GC; %d/%d bytes (used/threshold)", usage, threshold)
			forceGC(&memstats)

		case <-gcTriggered:
			notifyGC()

			renewThreshold()

			stopTimer()

		case <-_watchdog.closing:
			stopTimer()
			return
		}
	}
}

// forceGC forces a manual GC.
func forceGC(memstats *runtime.MemStats) {
	Logger.Infof("watchdog is forcing GC")

	startNotify := time.Now()
	notifyForcedGC()
	// it's safe to assume that the finalizer will attempt to run before
	// runtime.GC() returns because runtime.GC() waits for the sweep phase to
	// finish before returning.
	// finalizers are run in the sweep phase.
	start := time.Now()
	notificationsTook := start.Sub(startNotify)
	ForcedGCFunc()
	took := time.Since(start)

	memstatsFn(memstats)
	Logger.Infof("watchdog-triggered GC finished; notifications took: %s, took: %s; current heap allocated: %d bytes", notificationsTook, took, memstats.HeapAlloc)
}

func setupGCSentinel(gcTriggered chan struct{}) {
	logger := Logger

	// this non-zero sized struct is used as a sentinel to detect when a GC
	// run has finished, by setting and resetting a finalizer on it.
	// it essentially creates a GC notification "flywheel"; every GC will
	// trigger this finalizer, which will reset itself so it gets notified
	// of the next GC, breaking the cycle when the watchdog is stopped.
	type sentinel struct{ a *int }
	var finalizer func(o *sentinel)
	finalizer = func(o *sentinel) {
		_watchdog.lk.Lock()
		defer _watchdog.lk.Unlock()

		if _watchdog.state != stateRunning {
			// this GC triggered after the watchdog was stopped; ignore
			// and do not reset the finalizer.
			return
		}

		// reset so it triggers on the next GC.
		runtime.SetFinalizer(o, finalizer)

		select {
		case gcTriggered <- struct{}{}:
		default:
			logger.Warnf("failed to queue gc trigger; channel backlogged")
		}
	}

	runtime.SetFinalizer(&sentinel{}, finalizer) // start the flywheel.
}

func start(scope UtilizationType) error {
	_watchdog.lk.Lock()
	defer _watchdog.lk.Unlock()

	if _watchdog.state != stateUnstarted {
		return ErrAlreadyStarted
	}

	_watchdog.state = stateRunning
	_watchdog.scope = scope
	_watchdog.closing = make(chan struct{})

	initHeapProfileCapture()

	return nil
}

func stop() {
	_watchdog.lk.Lock()
	defer _watchdog.lk.Unlock()

	if _watchdog.state != stateRunning {
		return
	}

	close(_watchdog.closing)
	_watchdog.wg.Wait()
	_watchdog.state = stateUnstarted
}

func initHeapProfileCapture() {
	if HeapProfileDir == "" || HeapProfileThreshold <= 0 {
		Logger.Debugf("heap profile capture disabled")
		return
	}
	if HeapProfileThreshold >= 1 {
		Logger.Warnf("failed to initialize heap profile capture: threshold must be 0 < t < 1")
		return
	}
	if fi, err := os.Stat(HeapProfileDir); os.IsNotExist(err) {
		if err := os.MkdirAll(HeapProfileDir, 0777); err != nil {
			Logger.Warnf("failed to initialize heap profile capture: failed to create dir: %s; err: %s", HeapProfileDir, err)
			return
		}
	} else if err != nil {
		Logger.Warnf("failed to initialize heap profile capture: failed to stat path: %s; err: %s", HeapProfileDir, err)
		return
	} else if !fi.IsDir() {
		Logger.Warnf("failed to initialize heap profile capture: path exists but is not a directory: %s", HeapProfileDir)
		return
	}
	// all good, set the amount of heap profile captures left.
	_watchdog.hpleft = HeapProfileMaxCaptures
	Logger.Infof("initialized heap profile capture; threshold: %f; max captures: %d; dir: %s", HeapProfileThreshold, HeapProfileMaxCaptures, HeapProfileDir)
}

func maybeCaptureHeapProfile(usage, limit uint64) {
	if _watchdog.hpleft <= 0 {
		// nothing to do; no captures remaining (or captures disabled), or
		// already captured a heap profile for this episode.
		return
	}
	if float64(usage)/float64(limit) < HeapProfileThreshold {
		// we are below the threshold, reset the hpcurr flag.
		_watchdog.hpcurr = false
		return
	}
	// we are above the threshold.
	if _watchdog.hpcurr {
		return // we've already captured this episode, skip.
	}

	path := filepath.Join(HeapProfileDir, time.Now().Format(time.RFC3339Nano)+".heap")
	file, err := os.Create(path)
	if err != nil {
		Logger.Warnf("failed to create heap profile file; path: %s; err: %s", path, err)
		return
	}
	defer file.Close()

	if err = pprof.WriteHeapProfile(file); err != nil {
		Logger.Warnf("failed to write heap profile; path: %s; err: %s", path, err)
		return
	}

	Logger.Infof("heap profile captured; path: %s", path)
	_watchdog.hpcurr = true
	_watchdog.hpleft--
}

func wdrecover() {
	if r := recover(); r != nil {
		msg := fmt.Sprintf("WATCHDOG PANICKED; recovered but watchdog is disarmed: %s", r)
		if Logger != nil {
			Logger.Errorf(msg)
		} else {
			_, _ = fmt.Fprintln(os.Stderr, msg)
		}
	}
}
