package opts

import (
	"encoding/csv"
	"fmt"
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

func Unmarshal(src map[string]string, dest any) error {
	var kvs []kv
	for k, v := range src {
		kvs = append(kvs, kv{key: k, value: v})
	}
	return unmarshal(kvs, dest)
}

func UnmarshalCSV(src string, dest any) error {
	if src == "" {
		return nil
	}

	r := csv.NewReader(strings.NewReader(src))
	entries, err := r.Read()
	if err != nil {
		return err
	}

	var kvs []kv
	for _, entry := range entries {
		var k, v string
		parts := strings.SplitN(entry, "=", 2)
		k = strings.ToLower(parts[0])
		if len(parts) > 1 {
			v = parts[1]
		}
		kvs = append(kvs, kv{key: k, value: v})
	}
	return unmarshal(kvs, dest)
}

type kv struct {
	key   string
	value string
}

func unmarshal(src []kv, dest any) error {
	v := reflect.ValueOf(dest)

	u := unmarshaler{}
	if err := u.setup(v); err != nil {
		return err
	}

	if src == nil {
		return nil
	}

	handled := map[string]struct{}{}
	for _, kv := range src {
		key := kv.key
		value := kv.value
		handled[key] = struct{}{}

		field, ok := u.fields[key]
		if !ok {
			for pat, f := range u.fields {
				ok, err := path.Match(pat, key)
				if err != nil {
					return err
				}
				if ok {
					field = f
					break
				}
			}
		}
		if field == nil {
			if u.remain == nil {
				return errors.Errorf("key %q not present", key)
			}
			if err := u.remain(key, value); err != nil {
				return err
			}
			continue
		}

		if err := field(key, value); err != nil {
			return err
		}
	}

	for _, required := range u.required {
		if _, ok := handled[required]; !ok {
			return errors.Errorf("key %q was required but was not present", required)
		}
	}

	return nil
}

type setter = func(key, value string) error

type unmarshaler struct {
	fields   map[string]setter
	remain   setter
	required []string
}

func (u *unmarshaler) setup(v reflect.Value) error {
	if u.fields == nil {
		u.fields = map[string]setter{}
	}

	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return errors.New("cannot unmarshal into non-struct")
	}

	infos, err := getInfo(v)
	if err != nil {
		return err
	}

	for _, info := range infos {
		if info.Remain {
			remain, err := makeRemain(info.field.Type, info.vfield)
			if err != nil {
				return err
			}
			u.remain = remain
			continue
		}

		if info.Squash {
			if err := u.setup(info.vfield); err != nil {
				return err
			}
			continue
		}

		if info.Required {
			u.required = append(u.required, info.Key)
		}

		if info.Key == "" {
			continue
		}

		if info.marshaller != nil {
			u.fields[info.Key] = info.marshaller
		} else {
			setter, err := makeSetter(info.field.Type, info.vfield)
			if err != nil {
				return err
			}
			u.fields[info.Key] = setter
		}

		if info.Choices != nil {
			u.fields[info.Key] = makeValidator(u.fields[info.Key], info)
		}
	}

	return nil
}

func makeSetter(t reflect.Type, v reflect.Value) (setter, error) {
	switch t.Kind() {
	case reflect.String:
		return func(key, value string) error {
			v.SetString(value)
			return nil
		}, nil
	case reflect.Bool:
		return func(key, value string) error {
			if value == "" {
				v.SetBool(true)
				return nil
			}
			valueBool, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			v.SetBool(valueBool)
			return nil
		}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(key, value string) error {
			valueInt, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return err
			}
			v.SetInt(valueInt)
			return nil
		}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return func(key, value string) error {
			valueUint, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return err
			}
			v.SetUint(valueUint)
			return nil
		}, nil
	case reflect.Float32, reflect.Float64:
		return func(key, value string) error {
			valueFloat, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			v.SetFloat(valueFloat)
			return nil
		}, nil
	case reflect.Pointer:
		tmp := reflect.New(t.Elem())
		setter, err := makeSetter(t.Elem(), tmp.Elem())
		if err != nil {
			return nil, err
		}
		return func(key, value string) error {
			if err := setter(key, value); err != nil {
				return err
			}
			v.Set(tmp)
			return nil
		}, nil
	default:
		return nil, errors.Errorf("unknown field type %q", v.Type())
	}
}

func makeRemain(t reflect.Type, v reflect.Value) (setter, error) {
	switch t.Kind() {
	case reflect.String:
		return func(key, value string) error {
			s := v.String()
			if len(s) > 0 {
				s += ","
			}
			s += fmt.Sprintf("%s=%s", key, value)
			v.SetString(s)
			return nil
		}, nil
	case reflect.Slice:
		if t.Elem().Kind() != reflect.String {
			break
		}
		return func(key, value string) error {
			ss := v.Interface().([]string)
			ss = append(ss, fmt.Sprintf("%s=%s", key, value))
			v.Set(reflect.ValueOf(ss))
			return nil
		}, nil
	case reflect.Map:
		if t.Elem().Kind() != reflect.String || t.Key().Kind() != reflect.String {
			break
		}
		return func(key, value string) error {
			ss := v.Interface().(map[string]string)
			if ss == nil {
				ss = map[string]string{}
			}
			ss[key] = value
			v.Set(reflect.ValueOf(ss))
			return nil
		}, nil
	}
	return nil, errors.Errorf("remain cannot be %q", t)
}

func makeValidator(set setter, info OptInfo) setter {
	if info.Choices == nil {
		return set
	}

	choices := map[string]struct{}{}
	for _, choice := range info.Choices {
		choices[choice] = struct{}{}
	}
	return func(key, value string) error {
		if _, ok := choices[value]; !ok {
			return errors.Errorf("%q is not a valid choice for %s", value, info.Key)
		}
		return set(key, value)
	}
}
