package llb

import (
	"fmt"

	"github.com/google/shlex"
	"github.com/moby/buildkit/util/system"
)

func NewMeta(args ...string) Meta {
	m := Meta{}
	m, _ = AddEnv("PATH", system.DefaultPathEnv)(m)
	m, _ = Args(args...)(m)
	m, _ = Dir("/")(m)
	return m
}

type Meta struct {
	args []string
	env  envList
	cwd  string
}

func AddEnv(key, value string) RunOption {
	return AddEnvf(key, value)
}
func AddEnvf(key, value string, v ...interface{}) RunOption {
	return func(m Meta) (Meta, error) {
		m.env = m.env.AddOrReplace(key, fmt.Sprintf(value, v...))
		return m, nil
	}
}

func ClearEnv() RunOption {
	return func(m Meta) (Meta, error) {
		m.env = NewMeta().env
		return m, nil
	}
}

func DelEnv(key string) RunOption {
	return func(m Meta) (Meta, error) {
		m.env = m.env.Delete(key)
		return m, nil
	}
}

func Args(args ...string) RunOption {
	return func(m Meta) (Meta, error) {
		m.args = args
		return m, nil
	}
}

func Dir(str string) RunOption {
	return Dirf(str)
}
func Dirf(str string, v ...interface{}) RunOption {
	return func(m Meta) (Meta, error) {
		m.cwd = fmt.Sprintf(str, v...)
		return m, nil
	}
}

func Reset(s *State) RunOption {
	return func(m Meta) (Meta, error) {
		if s == nil {
			return NewMeta(), nil
		}
		return s.metaNext, nil
	}
}

func (m Meta) Env(key string) (string, bool) {
	return m.env.Get(key)
}

func (m Meta) Dir() string {
	return m.cwd
}

func (m Meta) Args() []string {
	return append([]string{}, m.args...)
}

func Shlex(str string) RunOption {
	return Shlexf(str)
}

func Shlexf(str string, v ...interface{}) RunOption {
	return func(m Meta) (Meta, error) {
		args, err := shlex.Split(fmt.Sprintf(str, v...))
		if err != nil {
			return m, err
		}
		return Args(args...)(m)
	}
}

type envList []keyValue

type keyValue struct {
	key   string
	value string
}

func (e envList) AddOrReplace(k, v string) envList {
	e = e.Delete(k)
	e = append(e, keyValue{key: k, value: v})
	return e
}

func (e envList) Delete(k string) envList {
	e = append([]keyValue(nil), e...)
	if i, ok := e.index(k); ok {
		return append(e[:i], e[i+1:]...)
	}
	return e
}

func (e envList) Get(k string) (string, bool) {
	if index, ok := e.index(k); ok {
		return e[index].value, true
	}
	return "", false
}

func (e envList) index(k string) (int, bool) {
	for i, kv := range e {
		if kv.key == k {
			return i, true
		}
	}
	return -1, false
}

func (e envList) ToArray() []string {
	out := make([]string, 0, len(e))
	for _, kv := range e {
		out = append(out, kv.key+"="+kv.value)
	}
	return out
}
