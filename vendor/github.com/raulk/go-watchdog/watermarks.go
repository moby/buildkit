package watchdog

// NewWatermarkPolicy creates a watchdog policy that schedules GC at concrete
// watermarks. When queried, it will determine the next trigger point based
// on the current utilisation. If the last watermark is surpassed,
// the policy will be disarmed. It is recommended to set an extreme watermark
// as the last element (e.g. 0.99) to prevent the policy from disarming too soon.
func NewWatermarkPolicy(watermarks ...float64) PolicyCtor {
	return func(limit uint64) (Policy, error) {
		p := new(watermarkPolicy)
		p.limit = limit
		p.thresholds = make([]uint64, 0, len(watermarks))
		for _, m := range watermarks {
			p.thresholds = append(p.thresholds, uint64(float64(limit)*m))
		}
		Logger.Infof("initialized watermark watchdog policy; watermarks: %v; thresholds: %v", p.watermarks, p.thresholds)
		return p, nil
	}
}

type watermarkPolicy struct {
	// watermarks are the percentual amounts of limit.
	watermarks []float64
	// thresholds are the absolute trigger points of this policy.
	thresholds []uint64
	limit      uint64
}

var _ Policy = (*watermarkPolicy)(nil)

func (w *watermarkPolicy) Evaluate(_ UtilizationType, used uint64) (next uint64) {
	Logger.Debugf("watermark policy: evaluating; utilization: %d/%d (used/limit)", used, w.limit)
	var i int
	for ; i < len(w.thresholds); i++ {
		t := w.thresholds[i]
		if used < t {
			return t
		}
	}
	// we reached the maximum threshold, so we disable this policy.
	return PolicyTempDisabled
}
