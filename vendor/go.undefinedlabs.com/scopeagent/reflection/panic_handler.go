package reflection

import (
	"reflect"
	"sync"
	"unsafe"
	_ "unsafe"

	"github.com/undefinedlabs/go-mpatch"
)

var (
	patchOnPanic    *mpatch.Patch
	mOnPanic        sync.Mutex
	onPanicHandlers []func(e interface{})

	patchOnExit   *mpatch.Patch
	mOnExit       sync.Mutex
	onExitHandler []func(e interface{})
)

// Adds a global panic handler (this handler will be executed before any recover call)
func AddPanicHandler(fn func(interface{})) {
	mOnPanic.Lock()
	defer mOnPanic.Unlock()
	if patchOnPanic == nil {
		gp := lgopanic
		np, err := mpatch.PatchMethodByReflect(reflect.Method{Func: reflect.ValueOf(gp)}, gopanic)
		if err == nil {
			patchOnPanic = np
		}
	}
	onPanicHandlers = append(onPanicHandlers, fn)
}

// Adds a global panic handler before process kill (this handler will be executed if not recover is set before the process exits)
func AddOnPanicExitHandler(fn func(interface{})) {
	mOnExit.Lock()
	defer mOnExit.Unlock()
	if patchOnExit == nil {
		gp := lpreprintpanics
		np, err := mpatch.PatchMethodByReflect(reflect.Method{Func: reflect.ValueOf(gp)}, preprintpanics)
		if err == nil {
			patchOnExit = np
		}
	}
	onExitHandler = append(onExitHandler, fn)
}

func gopanic(e interface{}) {
	mOnPanic.Lock()
	defer mOnPanic.Unlock()
	for _, fn := range onPanicHandlers {
		fn(e)
	}
	patchOnPanic.Unpatch()
	defer patchOnPanic.Patch()
	lgopanic(e)
}

func preprintpanics(p *_panic) {
	mOnExit.Lock()
	defer mOnExit.Unlock()
	for _, fn := range onExitHandler {
		fn(p.arg)
	}
	patchOnExit.Unpatch()
	defer patchOnExit.Patch()
	lpreprintpanics(p)
}

//go:linkname lgopanic runtime.gopanic
func lgopanic(e interface{})

//go:linkname lpreprintpanics runtime.preprintpanics
func lpreprintpanics(p *_panic)

type _panic struct {
	argp      unsafe.Pointer // pointer to arguments of deferred call run during panic; cannot move - known to liblink
	arg       interface{}    // argument to panic
	link      *_panic        // link to earlier panic
	recovered bool           // whether this panic is over
	aborted   bool           // the panic was aborted
}
