package eventbus

type subSettings struct {
	buffer int
}

var subSettingsDefault = subSettings{
	buffer: 16,
}

func BufSize(n int) func(interface{}) error {
	return func(s interface{}) error {
		s.(*subSettings).buffer = n
		return nil
	}
}

type emitterSettings struct {
	makeStateful bool
}

// Stateful is an Emitter option which makes the eventbus channel
// 'remember' last event sent, and when a new subscriber joins the
// bus, the remembered event is immediately sent to the subscription
// channel.
//
// This allows to provide state tracking for dynamic systems, and/or
// allows new subscribers to verify that there are Emitters on the channel
func Stateful(s interface{}) error {
	s.(*emitterSettings).makeStateful = true
	return nil
}
