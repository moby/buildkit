package reflection

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// Gets the type pointer to a field name
func GetTypePointer(i interface{}, fieldName string) (reflect.Type, error) {
	typeOf := reflect.Indirect(reflect.ValueOf(i)).Type()
	if member, ok := typeOf.FieldByName(fieldName); ok {
		return reflect.PtrTo(member.Type), nil
	}
	return nil, errors.New("field can't be retrieved")
}

// Gets a pointer of a private or public field in any struct
func GetFieldPointerOf(i interface{}, fieldName string) (unsafe.Pointer, error) {
	val := reflect.Indirect(reflect.ValueOf(i))
	member := val.FieldByName(fieldName)
	if member.IsValid() {
		ptrToY := unsafe.Pointer(member.UnsafeAddr())
		return ptrToY, nil
	}
	return nil, errors.New("field can't be retrieved")
}

func GetTestMutex(t *testing.T) *sync.RWMutex {
	if ptr, err := GetFieldPointerOf(t, "mu"); err == nil {
		return (*sync.RWMutex)(ptr)
	}
	return nil
}

func AddToHelpersMap(t *testing.T, frameFnNames []string) {
	t.Helper()
	mu := GetTestMutex(t)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}

	pointer, err := GetFieldPointerOf(t, "helpers")
	if err != nil {
		return
	}

	helpers := *(*map[string]struct{})(pointer)
	for _, fnName := range frameFnNames {
		helpers[fnName] = struct{}{}
	}
}

func GetIsParallel(t *testing.T) bool {
	mu := GetTestMutex(t)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if pointer, err := GetFieldPointerOf(t, "isParallel"); err == nil {
		return *(*bool)(pointer)
	}
	return false
}

func GetTestStartTime(t *testing.T) (time.Time, error) {
	mu := GetTestMutex(t)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if pointer, err := GetFieldPointerOf(t, "start"); err == nil {
		return *(*time.Time)(pointer), nil
	} else {
		return time.Time{}, err
	}
}

func GetTestDuration(t *testing.T) (time.Duration, error) {
	mu := GetTestMutex(t)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if pointer, err := GetFieldPointerOf(t, "duration"); err == nil {
		return *(*time.Duration)(pointer), nil
	} else {
		return 0, err
	}
}

func GetBenchmarkMutex(b *testing.B) *sync.RWMutex {
	if ptr, err := GetFieldPointerOf(b, "mu"); err == nil {
		return (*sync.RWMutex)(ptr)
	}
	return nil
}

func GetParentBenchmark(b *testing.B) *testing.B {
	mu := GetBenchmarkMutex(b)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if ptr, err := GetFieldPointerOf(b, "parent"); err == nil {
		return *(**testing.B)(ptr)
	}
	return nil
}

func GetBenchmarkSuiteName(b *testing.B) string {
	mu := GetBenchmarkMutex(b)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if ptr, err := GetFieldPointerOf(b, "importPath"); err == nil {
		return *(*string)(ptr)
	}
	return ""
}

func GetBenchmarkHasSub(b *testing.B) int32 {
	mu := GetBenchmarkMutex(b)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if ptr, err := GetFieldPointerOf(b, "hasSub"); err == nil {
		return *(*int32)(ptr)
	}
	return 0
}

//Get benchmark result from the private result field in testing.B
func GetBenchmarkResult(b *testing.B) (*testing.BenchmarkResult, error) {
	mu := GetBenchmarkMutex(b)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if ptr, err := GetFieldPointerOf(b, "result"); err == nil {
		return (*testing.BenchmarkResult)(ptr), nil
	} else {
		return nil, err
	}
}
