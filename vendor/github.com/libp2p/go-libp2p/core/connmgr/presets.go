package connmgr

import (
	"math"
	"time"
)

// DecayNone applies no decay.
func DecayNone() DecayFn {
	return func(value DecayingValue) (_ int, rm bool) {
		return value.Value, false
	}
}

// DecayFixed subtracts from by the provided minuend, and deletes the tag when
// first reaching 0 or negative.
func DecayFixed(minuend int) DecayFn {
	return func(value DecayingValue) (_ int, rm bool) {
		v := value.Value - minuend
		return v, v <= 0
	}
}

// DecayLinear applies a fractional coefficient to the value of the current tag,
// rounding down via math.Floor. It erases the tag when the result is zero.
func DecayLinear(coef float64) DecayFn {
	return func(value DecayingValue) (after int, rm bool) {
		v := math.Floor(float64(value.Value) * coef)
		return int(v), v <= 0
	}
}

// DecayExpireWhenInactive expires a tag after a certain period of no bumps.
func DecayExpireWhenInactive(after time.Duration) DecayFn {
	return func(value DecayingValue) (_ int, rm bool) {
		rm = time.Until(value.LastVisit) >= after
		return 0, rm
	}
}

// BumpSumUnbounded adds the incoming value to the peer's score.
func BumpSumUnbounded() BumpFn {
	return func(value DecayingValue, delta int) (after int) {
		return value.Value + delta
	}
}

// BumpSumBounded keeps summing the incoming score, keeping it within a
// [min, max] range.
func BumpSumBounded(min, max int) BumpFn {
	return func(value DecayingValue, delta int) (after int) {
		v := value.Value + delta
		if v >= max {
			return max
		} else if v <= min {
			return min
		}
		return v
	}
}

// BumpOverwrite replaces the current value of the tag with the incoming one.
func BumpOverwrite() BumpFn {
	return func(value DecayingValue, delta int) (after int) {
		return delta
	}
}
