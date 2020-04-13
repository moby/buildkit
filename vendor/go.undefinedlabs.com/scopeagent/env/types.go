package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type (
	eVar struct {
		Key   string
		Raw   string
		IsSet bool
	}

	BooleanEnvVar struct {
		eVar
		Value bool
	}

	IntEnvVar struct {
		eVar
		Value int
	}

	StringEnvVar struct {
		eVar
		Value string
	}

	SliceEnvVar struct {
		eVar
		Value []string
	}

	MapEnvVar struct {
		eVar
		Value map[string]interface{}
	}
)

func newEVar(keys ...string) eVar {
	var e eVar
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		e = eVar{
			Key:   key,
			Raw:   value,
			IsSet: ok,
		}
		if ok {
			break
		}
	}
	return e
}

func newBooleanEnvVar(defaultValue bool, keys ...string) BooleanEnvVar {
	envVar := BooleanEnvVar{eVar: newEVar(keys...)}
	if !envVar.IsSet {
		envVar.Value = defaultValue
		return envVar
	}
	value, err := strconv.ParseBool(envVar.Raw)
	if err != nil {
		panic(fmt.Sprintf("unable to parse %s - should be 'true' or 'false'", envVar.Key))
	}
	envVar.Value = value
	return envVar
}

func newIntEnvVar(defaultValue int, keys ...string) IntEnvVar {
	envVar := IntEnvVar{eVar: newEVar(keys...)}
	if !envVar.IsSet {
		envVar.Value = defaultValue
		return envVar
	}
	value, err := strconv.ParseInt(envVar.Raw, 0, 0)
	if err != nil {
		panic(fmt.Sprintf("unable to parse %s - does not seem to be an int", envVar.Key))
	}
	envVar.Value = int(value)
	return envVar
}

func newStringEnvVar(defaultValue string, keys ...string) StringEnvVar {
	envVar := StringEnvVar{eVar: newEVar(keys...)}
	if !envVar.IsSet {
		envVar.Value = defaultValue
		return envVar
	}
	envVar.Value = envVar.Raw
	return envVar
}

func newSliceEnvVar(defaultValue []string, keys ...string) SliceEnvVar {
	envVar := SliceEnvVar{eVar: newEVar(keys...)}
	if !envVar.IsSet {
		envVar.Value = defaultValue
		return envVar
	}
	val := strings.Split(envVar.Raw, ",")
	for i := range val {
		val[i] = strings.TrimSpace(val[i])
	}
	envVar.Value = val
	return envVar
}

func newMapEnvVar(defaultValue map[string]interface{}, keys ...string) MapEnvVar {
	envVar := MapEnvVar{eVar: newEVar(keys...)}
	if !envVar.IsSet {
		envVar.Value = defaultValue
		return envVar
	}
	valItems := strings.Split(envVar.Raw, ",")
	for i := range valItems {
		valItems[i] = strings.TrimSpace(valItems[i])
	}
	val := map[string]interface{}{}
	for _, item := range valItems {
		itemArr := strings.Split(item, "=")
		if len(itemArr) == 2 {
			val[itemArr[0]] = os.ExpandEnv(itemArr[1])
		}
	}
	envVar.Value = val
	return envVar
}

// For use in if's

func (e *BooleanEnvVar) Tuple() (bool, bool) {
	return e.Value, e.IsSet
}
func (e *IntEnvVar) Tuple() (int, bool) {
	return e.Value, e.IsSet
}
func (e *StringEnvVar) Tuple() (string, bool) {
	return e.Value, e.IsSet
}
func (e *SliceEnvVar) Tuple() ([]string, bool) {
	return e.Value, e.IsSet
}
func (e *MapEnvVar) Tuple() (map[string]interface{}, bool) {
	return e.Value, e.IsSet
}
