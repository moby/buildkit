package config

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/transport"
)

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// checks if a function returns either the specified type or the specified type
// and an error.
func checkReturnType(fnType, tptType reflect.Type) error {
	switch fnType.NumOut() {
	case 2:
		if fnType.Out(1) != errorType {
			return fmt.Errorf("expected (optional) second return value from transport constructor to be an error")
		}

		fallthrough
	case 1:
		if !fnType.Out(0).Implements(tptType) {
			return fmt.Errorf("transport constructor returns %s which doesn't implement %s", fnType.Out(0), tptType)
		}
	default:
		return fmt.Errorf("expected transport constructor to return a transport and, optionally, an error")
	}
	return nil
}

// Handles return values with optional errors. That is, return values of the
// form `(something, error)` or just `something`.
//
// Panics if the return value isn't of the correct form.
func handleReturnValue(out []reflect.Value) (interface{}, error) {
	switch len(out) {
	case 2:
		err := out[1]
		if err != (reflect.Value{}) && !err.IsNil() {
			return nil, err.Interface().(error)
		}
		fallthrough
	case 1:
		tpt := out[0]

		// Check for nil value and nil error.
		if tpt == (reflect.Value{}) {
			return nil, fmt.Errorf("unspecified error")
		}
		switch tpt.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Func:
			if tpt.IsNil() {
				return nil, fmt.Errorf("unspecified error")
			}
		}

		return tpt.Interface(), nil
	default:
		panic("expected 1 or 2 return values from transport constructor")
	}
}

// calls the transport constructor and annotates the error with the name of the constructor.
func callConstructor(c reflect.Value, args []reflect.Value) (interface{}, error) {
	val, err := handleReturnValue(c.Call(args))
	if err != nil {
		name := runtime.FuncForPC(c.Pointer()).Name()
		if name != "" {
			// makes debugging easier
			return nil, fmt.Errorf("transport constructor %s failed: %s", name, err)
		}
	}
	return val, err
}

type constructor func(host.Host, transport.Upgrader, pnet.PSK, connmgr.ConnectionGater, network.ResourceManager) interface{}

func makeArgumentConstructors(fnType reflect.Type, argTypes map[reflect.Type]constructor) ([]constructor, error) {
	params := fnType.NumIn()
	if fnType.IsVariadic() {
		params--
	}
	out := make([]constructor, params)
	for i := range out {
		argType := fnType.In(i)
		c, ok := argTypes[argType]
		if !ok {
			return nil, fmt.Errorf("argument %d has an unexpected type %s", i, argType.Name())
		}
		out[i] = c
	}
	return out, nil
}

func getConstructorOpts(t reflect.Type, opts ...interface{}) ([]reflect.Value, error) {
	if !t.IsVariadic() {
		if len(opts) > 0 {
			return nil, errors.New("constructor doesn't accept any options")
		}
		return nil, nil
	}
	if len(opts) == 0 {
		return nil, nil
	}
	// variadic parameters always go last
	wantType := t.In(t.NumIn() - 1).Elem()
	values := make([]reflect.Value, 0, len(opts))
	for _, opt := range opts {
		val := reflect.ValueOf(opt)
		if opt == nil {
			return nil, errors.New("expected a transport option, got nil")
		}
		if val.Type() != wantType {
			return nil, fmt.Errorf("expected option of type %s, got %s", wantType, reflect.TypeOf(opt))
		}
		values = append(values, val.Convert(wantType))
	}
	return values, nil
}

// makes a transport constructor.
func makeConstructor(
	tpt interface{},
	tptType reflect.Type,
	argTypes map[reflect.Type]constructor,
	opts ...interface{},
) (func(host.Host, transport.Upgrader, pnet.PSK, connmgr.ConnectionGater, network.ResourceManager) (interface{}, error), error) {
	v := reflect.ValueOf(tpt)
	// avoid panicing on nil/zero value.
	if v == (reflect.Value{}) {
		return nil, fmt.Errorf("expected a transport or transport constructor, got a %T", tpt)
	}
	t := v.Type()
	if t.Kind() != reflect.Func {
		return nil, fmt.Errorf("expected a transport or transport constructor, got a %T", tpt)
	}

	if err := checkReturnType(t, tptType); err != nil {
		return nil, err
	}

	argConstructors, err := makeArgumentConstructors(t, argTypes)
	if err != nil {
		return nil, err
	}
	optValues, err := getConstructorOpts(t, opts...)
	if err != nil {
		return nil, err
	}

	return func(h host.Host, u transport.Upgrader, psk pnet.PSK, cg connmgr.ConnectionGater, rcmgr network.ResourceManager) (interface{}, error) {
		arguments := make([]reflect.Value, 0, len(argConstructors)+len(opts))
		for i, makeArg := range argConstructors {
			if arg := makeArg(h, u, psk, cg, rcmgr); arg != nil {
				arguments = append(arguments, reflect.ValueOf(arg))
			} else {
				// ValueOf an un-typed nil yields a zero reflect
				// value. However, we _want_ the zero value of
				// the _type_.
				arguments = append(arguments, reflect.Zero(t.In(i)))
			}
		}
		arguments = append(arguments, optValues...)
		return callConstructor(v, arguments)
	}, nil
}
