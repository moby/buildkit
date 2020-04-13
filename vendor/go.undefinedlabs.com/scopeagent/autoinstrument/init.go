package autoinstrument

import (
	"reflect"
	"sync"
	"testing"

	"github.com/undefinedlabs/go-mpatch"

	"go.undefinedlabs.com/scopeagent"
	"go.undefinedlabs.com/scopeagent/agent"
	"go.undefinedlabs.com/scopeagent/instrumentation"
	scopetesting "go.undefinedlabs.com/scopeagent/instrumentation/testing"
)

var (
	once sync.Once
)

func init() {
	once.Do(func() {
		var m *testing.M
		var mRunMethod reflect.Method
		var ok bool
		mType := reflect.TypeOf(m)
		if mRunMethod, ok = mType.MethodByName("Run"); !ok {
			return
		}

		var runPatch *mpatch.Patch
		var err error
		runPatch, err = mpatch.PatchMethodByReflect(mRunMethod, func(m *testing.M) int {
			logOnError(runPatch.Unpatch())
			defer func() {
				logOnError(runPatch.Patch())
			}()
			scopetesting.PatchTestingLogger()
			defer scopetesting.UnpatchTestingLogger()
			return scopeagent.Run(m, agent.WithGlobalPanicHandler())
		})
		logOnError(err)
	})
}

func logOnError(err error) {
	if err != nil {
		instrumentation.Logger().Println(err)
	}
}
