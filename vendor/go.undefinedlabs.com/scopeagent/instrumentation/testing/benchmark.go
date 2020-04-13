package testing

import (
	"context"
	"math"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentracing/opentracing-go"

	"go.undefinedlabs.com/scopeagent/instrumentation"
	"go.undefinedlabs.com/scopeagent/reflection"
	"go.undefinedlabs.com/scopeagent/runner"
)

type (
	Benchmark struct {
		b *testing.B
	}
)

var (
	benchmarkMapMutex  sync.RWMutex
	benchmarkMap       = map[*testing.B]*Benchmark{}
	benchmarkNameRegex = regexp.MustCompile(`([\w-_:!@#\$%&()=]*)(\/\*\&\/)?`)
)

// Starts a new benchmark using a pc as caller
func StartBenchmark(b *testing.B, pc uintptr, benchFunc func(b *testing.B)) {
	if !hasBenchmark(b) {
		// If the current benchmark is not instrumented, we instrument it.
		startBenchmark(b, pc, benchFunc)
	} else {
		// If the benchmark is already instrumented, we passthrough to the benchFunc
		benchFunc(b)
	}
}

// Runs an auto instrumented sub benchmark
func (bench *Benchmark) Run(name string, f func(b *testing.B)) bool {
	pc, _, _, _ := runtime.Caller(1)
	return bench.b.Run(name, func(innerB *testing.B) {
		startBenchmark(innerB, pc, f)
	})
}

// Adds a benchmark struct to the map
func addBenchmark(b *testing.B, value *Benchmark) {
	benchmarkMapMutex.Lock()
	defer benchmarkMapMutex.Unlock()
	benchmarkMap[b] = value
}

// Gets if the benchmark struct exist
func hasBenchmark(b *testing.B) bool {
	benchmarkMapMutex.RLock()
	defer benchmarkMapMutex.RUnlock()
	_, ok := benchmarkMap[b]
	return ok
}

// Gets the Benchmark struct from *testing.Benchmark
func GetBenchmark(b *testing.B) *Benchmark {
	benchmarkMapMutex.RLock()
	defer benchmarkMapMutex.RUnlock()
	if bench, ok := benchmarkMap[b]; ok {
		return bench
	}
	return &Benchmark{b: b}
}

func startBenchmark(b *testing.B, pc uintptr, benchFunc func(b *testing.B)) {
	var bChild *testing.B
	b.ReportAllocs()
	b.ResetTimer()
	startTime := time.Now()
	result := b.Run("*&", func(b1 *testing.B) {
		addBenchmark(b1, &Benchmark{b: b1})
		benchFunc(b1)
		bChild = b1
	})
	if bChild == nil {
		return
	}
	if reflection.GetBenchmarkHasSub(bChild) > 0 {
		return
	}
	results, err := reflection.GetBenchmarkResult(bChild)
	if err != nil {
		instrumentation.Logger().Printf("Error while extracting the benchmark result object: %v\n", err)
		return
	}

	// Extracting the benchmark func name (by removing any possible sub-benchmark suffix `{bench_func}/{sub_benchmark}`)
	// to search the func source code bounds and to calculate the package name.
	fullTestName := runner.GetOriginalTestName(b.Name())

	// We detect if the parent benchmark is instrumented, and if so we remove the "*" SubBenchmark from the previous instrumentation
	parentBenchmark := reflection.GetParentBenchmark(b)
	if parentBenchmark != nil && hasBenchmark(parentBenchmark) {
		var nameSegments []string
		for _, match := range benchmarkNameRegex.FindAllStringSubmatch(fullTestName, -1) {
			if match[1] != "" {
				nameSegments = append(nameSegments, match[1])
			}
		}
		fullTestName = strings.Join(nameSegments, "/")
	}
	packageName := reflection.GetBenchmarkSuiteName(b)
	pName, _, tCode := getPackageAndNameAndBoundaries(pc)

	if packageName == "" {
		packageName = pName
	}

	oTags := opentracing.Tags{
		"span.kind":      "test",
		"test.name":      fullTestName,
		"test.suite":     packageName,
		"test.framework": "testing",
		"test.language":  "go",
		"test.type":      "benchmark",
	}

	if tCode != "" {
		oTags["test.code"] = tCode
	}

	var startOptions []opentracing.StartSpanOption
	startOptions = append(startOptions, oTags, opentracing.StartTime(startTime))

	span, _ := opentracing.StartSpanFromContextWithTracer(context.Background(), instrumentation.Tracer(), fullTestName, startOptions...)
	span.SetBaggageItem("trace.kind", "test")
	avg := math.Round((float64(results.T.Nanoseconds())/float64(results.N))*100) / 100
	span.SetTag("benchmark.runs", results.N)
	span.SetTag("benchmark.duration.mean", avg)
	span.SetTag("benchmark.memory.mean_allocations", results.AllocsPerOp())
	span.SetTag("benchmark.memory.mean_bytes_allocations", results.AllocedBytesPerOp())
	if result {
		span.SetTag("test.status", "PASS")
	} else {
		span.SetTag("test.status", "FAIL")
	}
	span.FinishWithOptions(opentracing.FinishOptions{
		FinishTime: startTime.Add(results.T),
	})
}
