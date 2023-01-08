package watchdog

// NewAdaptivePolicy creates a policy that forces GC when the usage surpasses a
// user-configured percentage (factor) of the available memory.
//
// This policy recalculates the next target as usage+(limit-usage)*factor.
func NewAdaptivePolicy(factor float64) PolicyCtor {
	return func(limit uint64) (Policy, error) {
		return &adaptivePolicy{
			factor: factor,
			limit:  limit,
		}, nil
	}
}

type adaptivePolicy struct {
	factor float64
	limit  uint64
}

var _ Policy = (*adaptivePolicy)(nil)

func (p *adaptivePolicy) Evaluate(_ UtilizationType, used uint64) (next uint64) {
	if used >= p.limit {
		return used
	}

	available := float64(p.limit) - float64(used)
	next = used + uint64(available*p.factor)
	return next
}
