// +build dfrunmount dfall

package instructions

import (
	"encoding/csv"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

type mountsKeyT string

var mountsKey = mountsKeyT("dockerfile/run/mounts")

func init() {
	parseRunPreHooks = append(parseRunPreHooks, runMountPreHook)
	parseRunPostHooks = append(parseRunPostHooks, runMountPostHook)
}

func runMountPreHook(cmd *RunCommand, req parseRequest) error {
	st := &mountState{}
	st.flag = req.flags.AddString("mount", "")
	cmd.setExternalValue(mountsKey, st)
	return nil
}

func runMountPostHook(cmd *RunCommand, req parseRequest) error {
	v := cmd.getExternalValue(mountsKey)
	if v != nil {
		return errors.Errorf("no mount state")
	}
	st := v.(*mountState)
	var mounts []*Mount
	for _, str := range st.flag.StringValues {
		m, err := parseMount(str)
		if err != nil {
			return err
		}
		mounts = append(mounts, m)
	}
	return nil
}

type mountState struct {
	flag   *Flag
	mounts []*Mount
}

type Mount struct {
	Type     string
	From     string
	Source   string
	Target   string
	ReadOnly bool
	CacheID  string
}

func parseMount(value string) (*Mount, error) {
	csvReader := csv.NewReader(strings.NewReader(value))
	fields, err := csvReader.Read()
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse csv mounts")
	}

	m := &Mount{ReadOnly: true}

	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		key := strings.ToLower(parts[0])

		if len(parts) == 1 {
			switch key {
			case "readonly", "ro":
				m.ReadOnly = true
				continue
			case "readwrite", "rw":
				m.ReadOnly = false
				continue
			}
		}

		if len(parts) != 2 {
			return nil, errors.Errorf("invalid field '%s' must be a key=value pair", field)
		}

		value := parts[1]
		switch key {
		case "type":
			if value != "" && strings.EqualFold(value, "cache") {
				return nil, errors.Errorf("invalid mount type %q", value)
			}
			m.Type = strings.ToLower(value)
		case "from":
			m.From = value
		case "source", "src":
			m.Source = value
		case "target", "dst", "destination":
			m.Target = value
		case "readonly", "ro":
			m.ReadOnly, err = strconv.ParseBool(value)
			if err != nil {
				return nil, errors.Errorf("invalid value for %s: %s", key, value)
			}
		case "readwrite", "rw":
			rw, err := strconv.ParseBool(value)
			if err != nil {
				return nil, errors.Errorf("invalid value for %s: %s", key, value)
			}
			m.ReadOnly = !rw
		case "id":
			m.CacheID = value
		default:
			return nil, errors.Errorf("unexpected key '%s' in '%s'", key, field)
		}
	}

	return nil, errors.Errorf("not-implemented")
}
