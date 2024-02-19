package instructions

import (
	"strconv"
	"strings"

	"github.com/docker/go-units"
	"github.com/moby/buildkit/frontend/dockerui/types"
	"github.com/moby/buildkit/util/suggest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/go-csvvalue"
)

type MountType = types.MountType

const (
	MountTypeBind   = types.MountTypeBind
	MountTypeCache  = types.MountTypeCache
	MountTypeTmpfs  = types.MountTypeTmpfs
	MountTypeSecret = types.MountTypeSecret
	MountTypeSSH    = types.MountTypeSSH
)

var allowedMountTypes = map[MountType]struct{}{
	MountTypeBind:   {},
	MountTypeCache:  {},
	MountTypeTmpfs:  {},
	MountTypeSecret: {},
	MountTypeSSH:    {},
}

type ShareMode = types.ShareMode

const (
	MountSharingShared  = types.MountSharingShared
	MountSharingPrivate = types.MountSharingPrivate
	MountSharingLocked  = types.MountSharingLocked
)

var allowedSharingModes = map[ShareMode]struct{}{
	MountSharingShared:  {},
	MountSharingPrivate: {},
	MountSharingLocked:  {},
}

type mountsKeyT string

var mountsKey = mountsKeyT("dockerfile/run/mounts")

func init() {
	parseRunPreHooks = append(parseRunPreHooks, runMountPreHook)
	parseRunPostHooks = append(parseRunPostHooks, runMountPostHook)
}

func allShareModes() []string {
	types := make([]string, 0, len(allowedSharingModes))
	for k := range allowedSharingModes {
		types = append(types, string(k))
	}
	return types
}

func allMountTypes() []string {
	types := make([]string, 0, len(allowedMountTypes))
	for k := range allowedMountTypes {
		types = append(types, string(k))
	}
	return types
}

func runMountPreHook(cmd *RunCommand, req parseRequest) error {
	st := &mountState{}
	st.flag = req.flags.AddStrings("mount")
	cmd.setExternalValue(mountsKey, st)
	return nil
}

func runMountPostHook(cmd *RunCommand, req parseRequest) error {
	return setMountState(cmd, nil)
}

func setMountState(cmd *RunCommand, expander SingleWordExpander) error {
	st := getMountState(cmd)
	if st == nil {
		return errors.Errorf("no mount state")
	}
	var mounts []*Mount
	if hook := cmd.GetInstructionHook(); hook != nil && hook.Run != nil {
		for _, m := range hook.Run.Mounts {
			m := m
			if err := validateMount(&m, false); err != nil {
				return err
			}
			mounts = append(mounts, &m)
		}
	}
	for _, str := range st.flag.StringValues {
		m, err := parseMount(str, expander)
		if err != nil {
			return err
		}
		mounts = append(mounts, m)
	}
	st.mounts = mounts
	return nil
}

func getMountState(cmd *RunCommand) *mountState {
	v := cmd.getExternalValue(mountsKey)
	if v == nil {
		return nil
	}
	return v.(*mountState)
}

func GetMounts(cmd *RunCommand) []*Mount {
	return getMountState(cmd).mounts
}

type mountState struct {
	flag   *Flag
	mounts []*Mount
}

type Mount = types.Mount

func parseMount(val string, expander SingleWordExpander) (*Mount, error) {
	fields, err := csvvalue.Fields(val, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse csv mounts")
	}

	m := &Mount{Type: MountTypeBind}

	roAuto := true

	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		key = strings.ToLower(key)

		if !ok {
			if expander == nil {
				continue // evaluate later
			}
			switch key {
			case "readonly", "ro":
				m.ReadOnly = true
				roAuto = false
				continue
			case "readwrite", "rw":
				m.ReadOnly = false
				roAuto = false
				continue
			case "required":
				if m.Type == MountTypeSecret || m.Type == MountTypeSSH {
					m.Required = true
					continue
				} else {
					return nil, errors.Errorf("unexpected key '%s' for mount type '%s'", key, m.Type)
				}
			default:
				// any other option requires a value.
				return nil, errors.Errorf("invalid field '%s' must be a key=value pair", field)
			}
		}

		// check for potential variable
		if expander != nil {
			value, err = expander(value)
			if err != nil {
				return nil, err
			}
		} else if key == "from" {
			if idx := strings.IndexByte(value, '$'); idx != -1 && idx != len(value)-1 {
				return nil, errors.Errorf("'%s' doesn't support variable expansion, define alias stage instead", key)
			}
		} else {
			// if we don't have an expander, defer evaluation to later
			continue
		}

		switch key {
		case "type":
			v := MountType(strings.ToLower(value))
			if _, ok := allowedMountTypes[v]; !ok {
				return nil, suggest.WrapError(errors.Errorf("unsupported mount type %q", value), value, allMountTypes(), true)
			}
			m.Type = v
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
			roAuto = false
		case "readwrite", "rw":
			rw, err := strconv.ParseBool(value)
			if err != nil {
				return nil, errors.Errorf("invalid value for %s: %s", key, value)
			}
			m.ReadOnly = !rw
			roAuto = false
		case "required":
			if m.Type == MountTypeSecret || m.Type == MountTypeSSH {
				m.Required, err = strconv.ParseBool(value)
				if err != nil {
					return nil, errors.Errorf("invalid value for %s: %s", key, value)
				}
			} else {
				return nil, errors.Errorf("unexpected key '%s' for mount type '%s'", key, m.Type)
			}
		case "size":
			if m.Type == MountTypeTmpfs {
				m.SizeLimit, err = units.RAMInBytes(value)
				if err != nil {
					return nil, errors.Errorf("invalid value for %s: %s", key, value)
				}
			} else {
				return nil, errors.Errorf("unexpected key '%s' for mount type '%s'", key, m.Type)
			}
		case "id":
			m.CacheID = value
		case "sharing":
			v := ShareMode(strings.ToLower(value))
			if _, ok := allowedSharingModes[v]; !ok {
				return nil, suggest.WrapError(errors.Errorf("unsupported sharing value %q", value), value, allShareModes(), true)
			}
			m.CacheSharing = v
		case "mode":
			mode, err := strconv.ParseUint(value, 8, 32)
			if err != nil {
				return nil, errors.Errorf("invalid value %s for mode", value)
			}
			m.Mode = &mode
		case "uid":
			uid, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return nil, errors.Errorf("invalid value %s for uid", value)
			}
			m.UID = &uid
		case "gid":
			gid, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return nil, errors.Errorf("invalid value %s for gid", value)
			}
			m.GID = &gid
		case "env":
			m.Env = &value
		default:
			allKeys := []string{
				"type", "from", "source", "target", "readonly", "id", "sharing", "required", "size", "mode", "uid", "gid", "src", "dst", "destination", "ro", "rw", "readwrite", "env",
			}
			return nil, suggest.WrapError(errors.Errorf("unexpected key '%s' in '%s'", key, field), key, allKeys, true)
		}
	}

	if err = validateMount(m, roAuto); err != nil {
		return nil, err
	}
	return m, nil
}

func validateMount(m *Mount, roAuto bool) error {
	fileInfoAllowed := m.Type == MountTypeSecret || m.Type == MountTypeSSH || m.Type == MountTypeCache

	if !fileInfoAllowed {
		if m.Mode != nil {
			return errors.Errorf("mode not allowed for %q type mounts", m.Type)
		}
		if m.UID != nil {
			return errors.Errorf("uid not allowed for %q type mounts", m.Type)
		}
		if m.GID != nil {
			return errors.Errorf("gid not allowed for %q type mounts", m.Type)
		}
	}

	if roAuto {
		if m.Type == MountTypeCache || m.Type == MountTypeTmpfs {
			m.ReadOnly = false
		} else {
			m.ReadOnly = true
		}
	}

	if m.Type == MountTypeSecret {
		if m.From != "" {
			return errors.Errorf("secret mount should not have a from")
		}
		if m.CacheSharing != "" {
			return errors.Errorf("secret mount should not define sharing")
		}
		if m.Source == "" && m.Target == "" && m.CacheID == "" {
			return errors.Errorf("invalid secret mount. one of source, target required")
		}
		if m.Source != "" && m.CacheID != "" {
			return errors.Errorf("both source and id can't be set")
		}
	}

	if m.CacheSharing != "" && m.Type != MountTypeCache {
		return errors.Errorf("invalid cache sharing set for %v mount", m.Type)
	}

	return nil
}
