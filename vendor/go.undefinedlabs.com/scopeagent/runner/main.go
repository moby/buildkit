package runner

import (
	"fmt"
	"io/ioutil"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	goerrors "github.com/go-errors/errors"

	"go.undefinedlabs.com/scopeagent/reflection"
)

type (
	testRunner struct {
		m          *testing.M
		options    Options
		failed     bool
		failedLock sync.Mutex
	}
	testDescriptor struct {
		runner  *testRunner
		test    testing.InternalTest
		ran     int
		failed  bool
		flaky   bool
		error   bool
		skipped bool
	}
	Options struct {
		FailRetries int
		PanicAsFail bool
		Logger      *log.Logger
		OnPanic     func(t *testing.T, err interface{})
	}
)

var runner *testRunner
var runnerRegexName = regexp.MustCompile(`(?m)([\w -:_]*)\/\[runner.[\w:]*](\/[\w -:_]*)?`)

// Gets the test name
func GetOriginalTestName(name string) string {
	match := runnerRegexName.FindStringSubmatch(name)
	if match == nil || len(match) == 0 {
		return name
	}
	return match[1] + match[2]
}

// Runs a test suite
func Run(m *testing.M, options Options) int {
	if options.Logger == nil {
		options.Logger = log.New(ioutil.Discard, "", 0)
	}
	if options.OnPanic == nil {
		options.OnPanic = func(t *testing.T, err interface{}) {}
	}
	runner := &testRunner{
		m:       m,
		options: options,
		failed:  false,
	}
	runner.init(options.FailRetries > 0 || options.PanicAsFail)
	return runner.m.Run()
}

// Initialize test runner, replace the internal test with an indirection
func (r *testRunner) init(enableRunner bool) {
	if tPointer, err := reflection.GetFieldPointerOf(r.m, "tests"); err == nil {
		tests := make([]testing.InternalTest, 0)
		internalTests := (*[]testing.InternalTest)(tPointer)
		for _, test := range *internalTests {
			if enableRunner {
				td := &testDescriptor{
					runner: r,
					test:   test,
					ran:    0,
					failed: false,
				}
				tests = append(tests, testing.InternalTest{
					Name: test.Name,
					F:    td.run,
				})
			} else {
				cTest := test
				tests = append(tests, testing.InternalTest{
					Name: test.Name,
					F: func(t *testing.T) {
						defer func() {
							if rc := recover(); rc != nil {
								r.options.OnPanic(t, rc)
								panic(rc)
							}
						}()
						cTest.F(t)
					},
				})
			}
		}
		// Replace internal tests
		*internalTests = tests
	}
}

// Internal test runner, each test calls this method in order to handle retries and process exiting
func (td *testDescriptor) run(t *testing.T) {
	run := 1
	options := td.runner.options
	var innerError *goerrors.Error

	for {
		var innerTest *testing.T
		title := "Run"
		if run > 1 {
			title = "Retry:" + strconv.Itoa(run-1)
		}
		title = "[runner." + title + "]"
		t.Run(title, func(it *testing.T) {
			// We need to run another subtest in order to support t.Parallel()
			// https://stackoverflow.com/a/53950628
			setChattyFlag(it, false) // avoid the [exec] subtest in stdout
			it.Run("[exec]", func(gt *testing.T) {
				defer func() {
					rc := recover()
					if rc != nil {
						// using go-errors to preserve stacktrace
						innerError = goerrors.Wrap(rc, 2)
						gt.FailNow()
					}
				}()
				setChattyFlag(gt, true)                                       // enable inner test in stdout
				setTestName(gt, strings.Replace(it.Name(), "[exec]", "", -1)) // removes [exec] from name
				innerTest = gt
				td.test.F(gt)
			})
			if reflection.GetIsParallel(innerTest) && !reflection.GetIsParallel(t) {
				t.Parallel()
			}
		})
		if innerError != nil {
			if !options.PanicAsFail {
				options.OnPanic(t, innerError)
				panic(innerError.ErrorStack())
			}
			options.Logger.Printf("test '%s' %s - panic recover: %v", t.Name(), title, innerError)
			td.error = true
		}
		td.skipped = innerTest.Skipped()
		if td.skipped {
			t.SkipNow()
			break
		}
		td.ran++

		if innerTest.Failed() {
			// Current run failure
			td.failed = true
		} else if td.failed {
			// Current run ok but previous run with fail -> Flaky
			td.failed = false
			td.flaky = true
			options.Logger.Printf("test '%s' %s - is a flaky test!", t.Name(), title)
			break
		} else {
			// Current run ok and previous run (if any) not marked as failed
			break
		}

		if run > options.FailRetries {
			break
		}
		run++
	}

	// Set the global failed flag
	td.refreshGlobalFailedFlag(t)

	if td.error {
		if !options.PanicAsFail {
			// If after all recovers and retries the test finish with error and we have the exitOnError flag,
			// we panic with the latest recovered data
			options.OnPanic(t, innerError)
			panic(innerError)
		}
		fmt.Printf("panic info for test '%s': %v\n", t.Name(), innerError)
		options.Logger.Printf("panic info for test '%s': %v", t.Name(), innerError)
	}
	if !td.error && !td.failed {
		// If test pass or flaky
		setTestFailureFlag(t, false)
	}
}

func (td *testDescriptor) refreshGlobalFailedFlag(t *testing.T) {
	td.runner.failedLock.Lock()
	defer td.runner.failedLock.Unlock()
	td.runner.failed = td.runner.failed || td.failed || td.error
	tParent := getTestParent(t)
	if tParent != nil {
		setTestFailureFlag(tParent, td.runner.failed)
	}
}

// Sets the test failure flag
func setTestFailureFlag(t *testing.T, value bool) {
	mu := reflection.GetTestMutex(t)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}

	if ptr, err := reflection.GetFieldPointerOf(t, "failed"); err == nil {
		*(*bool)(ptr) = value
	}
}

// Gets the parent from a test
func getTestParent(t *testing.T) *testing.T {
	mu := reflection.GetTestMutex(t)
	if mu != nil {
		mu.RLock()
		defer mu.RUnlock()
	}

	if parentPtr, err := reflection.GetFieldPointerOf(t, "parent"); err == nil {
		parentTPointer := (**testing.T)(parentPtr)
		if parentTPointer != nil && *parentTPointer != nil {
			return *parentTPointer
		}
	}
	return nil
}

// Sets the chatty flag
func setChattyFlag(t *testing.T, value bool) {
	mu := reflection.GetTestMutex(t)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}

	if ptr, err := reflection.GetFieldPointerOf(t, "chatty"); err == nil {
		*(*bool)(ptr) = value
	}
}

// Sets the test name
func setTestName(t *testing.T, value string) {
	mu := reflection.GetTestMutex(t)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}

	if ptr, err := reflection.GetFieldPointerOf(t, "name"); err == nil {
		*(*string)(ptr) = value
	}
}
