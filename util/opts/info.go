package opts

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/pkg/errors"
)

type OptInfo struct {
	Key     string
	Type    string
	Default any

	Name string
	Help string

	Remain   bool
	Required bool
	Squash   bool
	Hidden   bool
	Choices  []string

	Children []OptInfo

	field      reflect.StructField
	vfield     reflect.Value
	marshaller setter
}

func Info(dest any) ([]OptInfo, error) {
	v := reflect.ValueOf(dest)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	return getInfoRecurse(v, true)
}

const (
	tagKey  = "opt"
	nameKey = "name"
	helpKey = "help"
)

func getInfo(value reflect.Value) ([]OptInfo, error) {
	return getInfoRecurse(value, false)
}

func getInfoRecurse(value reflect.Value, recurse bool) ([]OptInfo, error) {
	tp := value.Type()
	if tp.Kind() != reflect.Struct {
		return nil, errors.Errorf("is not struct")
	}

	var infos []OptInfo
	for i := 0; i < tp.NumField(); i++ {
		field := tp.Field(i)
		vfield := value.Field(i)
		if !field.IsExported() {
			continue
		}

		info := OptInfo{
			field:  field,
			vfield: vfield,
		}

		if field.Type.Kind() != reflect.Struct {
			info.Key = strings.ToLower(field.Name)
		}
		var tag []string
		if t, ok := field.Tag.Lookup(tagKey); ok {
			tag = strings.Split(t, ",")
		}
		if tag != nil {
			info.Key = tag[0]
			if info.Key == "-" {
				info.Key = ""
			}
			tag = tag[1:]
		}

		if t, ok := field.Tag.Lookup(nameKey); ok {
			info.Name = t
		}
		if t, ok := field.Tag.Lookup(helpKey); ok {
			info.Help = t
		}

		switch field.Type.Kind() {
		case reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
		default:
			// TODO: pointers give weird results here
			info.Type = strings.TrimLeft(fmt.Sprint(field.Type), "*")
			info.Default = vfield.Interface()
		}

		for _, tag := range tag {
			switch tag {
			case "remain":
				if info.Remain {
					return nil, errors.New("cannot have multiple remain fields")
				}
				info.Remain = true
			case "squash":
				info.Squash = true
				if recurse {
					children, err := getInfoRecurse(vfield, recurse)
					if err != nil {
						return nil, err
					}
					info.Children = children
				}
			case "required":
				info.Required = true
			case "hidden":
				info.Hidden = true
			default:
				choices := strings.Split(tag, "/")
				if len(choices) > 1 {
					info.Choices = choices
				}
			}
		}

		if method := value.Addr().MethodByName("Unmarshal" + field.Name); method.IsValid() && !method.IsZero() {
			setter, ok := method.Interface().(setter)
			if !ok {
				return nil, errors.Errorf("expected custom Unmarshal to be setter but got (%s)", method.Type())
			}
			info.marshaller = setter
		}

		infos = append(infos, info)
	}

	return infos, nil
}
