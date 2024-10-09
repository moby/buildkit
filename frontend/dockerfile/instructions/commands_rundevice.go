package instructions

import (
	"github.com/pkg/errors"
)

var deviceKey = "dockerfile/run/device"

func init() {
	parseRunPreHooks = append(parseRunPreHooks, runDevicePreHook)
	parseRunPostHooks = append(parseRunPostHooks, runDevicePostHook)
}

func runDevicePreHook(cmd *RunCommand, req parseRequest) error {
	st := &deviceState{}
	st.flag = req.flags.AddStrings("device")
	cmd.setExternalValue(deviceKey, st)
	return nil
}

func runDevicePostHook(cmd *RunCommand, req parseRequest) error {
	return setDeviceState(cmd)
}

func setDeviceState(cmd *RunCommand) error {
	st := getDeviceState(cmd)
	if st == nil {
		return errors.Errorf("no device state")
	}
	st.names = st.flag.StringValues
	return nil
}

func getDeviceState(cmd *RunCommand) *deviceState {
	v := cmd.getExternalValue(deviceKey)
	if v == nil {
		return nil
	}
	return v.(*deviceState)
}

func GetDevices(cmd *RunCommand) []string {
	return getDeviceState(cmd).names
}

type deviceState struct {
	flag  *Flag
	names []string
}
