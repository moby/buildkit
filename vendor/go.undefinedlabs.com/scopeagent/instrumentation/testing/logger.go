package testing

import (
	"reflect"
	"sync"
	"testing"
	"unsafe"

	"github.com/undefinedlabs/go-mpatch"

	"go.undefinedlabs.com/scopeagent/instrumentation"
	"go.undefinedlabs.com/scopeagent/reflection"
)

var (
	commonPtr          reflect.Type // *testing.common type
	patchLock          sync.Mutex
	patchesMutex       sync.RWMutex
	patches            = map[string]*mpatch.Patch{} // patches
	patchPointersMutex sync.RWMutex
	patchPointers      = map[uintptr]bool{} // pointers of patch funcs
)

func init() {
	// We get the *testing.common type to use in the patch method
	if cPtr, err := reflection.GetTypePointer(testing.T{}, "common"); err == nil {
		commonPtr = cPtr
	}
}

func PatchTestingLogger() {
	patchError()
	patchErrorf()
	patchFatal()
	patchFatalf()
	patchLog()
	patchLogf()
	patchSkip()
	patchSkipf()
}

func UnpatchTestingLogger() {
	patchLock.Lock()
	defer patchLock.Unlock()
	patchPointersMutex.Lock()
	defer patchPointersMutex.Unlock()
	for _, patch := range patches {
		logOnError(patch.Unpatch())
	}
	patches = map[string]*mpatch.Patch{}
	patchPointers = map[uintptr]bool{}
}

func patchError() {
	patch("Error", func(test *Test, args []interface{}) {
		test.t.Helper()
		test.Error(args...)
	})
}

func patchErrorf() {
	patch("Errorf", func(test *Test, args []interface{}) {
		test.t.Helper()
		format := args[0].(string)
		test.Errorf(format, args[1:]...)
	})
}

func patchFatal() {
	patch("Fatal", func(test *Test, args []interface{}) {
		test.t.Helper()
		test.Fatal(args...)
	})
}

func patchFatalf() {
	patch("Fatalf", func(test *Test, args []interface{}) {
		test.t.Helper()
		format := args[0].(string)
		test.Fatalf(format, args[1:]...)
	})
}

func patchLog() {
	patch("Log", func(test *Test, args []interface{}) {
		test.t.Helper()
		test.Log(args...)
	})
}

func patchLogf() {
	patch("Logf", func(test *Test, args []interface{}) {
		test.t.Helper()
		format := args[0].(string)
		test.Logf(format, args[1:]...)
	})
}

func patchSkip() {
	patch("Skip", func(test *Test, args []interface{}) {
		test.t.Helper()
		test.Skip(args...)
	})
}

func patchSkipf() {
	patch("Skipf", func(test *Test, args []interface{}) {
		test.t.Helper()
		format := args[0].(string)
		test.Skipf(format, args[1:]...)
	})
}

func createArgs(in []reflect.Value) []interface{} {
	var args []interface{}
	for _, item := range in {
		if item.Kind() == reflect.Slice {
			var itemArg []interface{}
			for i := 0; i < item.Len(); i++ {
				itemArg = append(itemArg, item.Index(i).Interface())
			}
			args = append(args, itemArg)
		} else {
			args = append(args, item.Interface())
		}
	}
	return args
}

func patch(methodName string, methodBody func(test *Test, argsValues []interface{})) {
	patchesMutex.Lock()
	defer patchesMutex.Unlock()
	patchPointersMutex.Lock()
	defer patchPointersMutex.Unlock()

	var method reflect.Method
	var ok bool
	if method, ok = commonPtr.MethodByName(methodName); !ok {
		return
	}

	var methodPatch *mpatch.Patch
	var err error
	methodPatch, err = mpatch.PatchMethodWithMakeFunc(method, func(in []reflect.Value) []reflect.Value {
		argIn := createArgs(in[1:])
		t := (*testing.T)(unsafe.Pointer(in[0].Pointer()))
		if t == nil {
			instrumentation.Logger().Println("testing.T is nil")
			return nil
		}

		t.Helper()
		reflection.AddToHelpersMap(t, []string{
			"reflect.callReflect",
			"reflect.makeFuncStub",
		})

		test := GetTest(t)
		if test == nil {
			instrumentation.Logger().Printf("test struct for %v doesn't exist\n", t.Name())
			return nil
		}
		methodBody(test, argIn)
		return nil
	})
	logOnError(err)
	if err == nil {
		patches[methodName] = methodPatch
		patchPointers[reflect.ValueOf(methodBody).Pointer()] = true
	}
}

func logOnError(err error) {
	if err != nil {
		instrumentation.Logger().Println(err)
	}
}

func isAPatchPointer(ptr uintptr) bool {
	patchPointersMutex.RLock()
	defer patchPointersMutex.RUnlock()
	if _, ok := patchPointers[ptr]; ok {
		return true
	}
	return false
}

func getMethodPatch(methodName string) *mpatch.Patch {
	patchesMutex.RLock()
	defer patchesMutex.RUnlock()
	return patches[methodName]
}
