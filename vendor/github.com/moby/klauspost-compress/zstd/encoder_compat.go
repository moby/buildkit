// This file is specific to github.com/moby/klauspost-compress. See README.md.

package zstd

import (
	"errors"
	"fmt"
)

// EncoderCompat selects the encoder output of an earlier release of this module.
//
// Encoding the same input with the same options and the same EncoderCompat gives the
// same bytes in every later release. Each constant is named after the first release
// that produced its output; releases that did not change the output have no constant.
// The numeric values are stable and can be stored.
//
// The guarantee covers successful use of the Encoder API. It does not cover
// the content of WithEncoderPadding (random), output after an option returned an error,
// output that the selected release itself could not decode, the snappy converter,
// dictionary building, or WithConcurrentBlocks with a window above 64 MiB.
// With WithConcurrentBlocks or a dictionary, also fix WithEncoderConcurrency:
// the output of those depends on the number of concurrent encoders.
type EncoderCompat uint

const (
	// EncoderCompatV1_20_2 produces the output of v1.20.2.
	// For non-empty input this also matches v1.18.5 to v1.20.1.
	EncoderCompatV1_20_2 EncoderCompat = 1

	// EncoderCompatV1_20_3 produces the output of v1.20.3 and of later releases
	// up to the next level. It reuses dictionary sequence tables and counts the exact table header
	// and flush costs when choosing sequence tables (upstream #1233).
	EncoderCompatV1_20_3 EncoderCompat = 2

	// EncoderCompatCurrent is the output this release produces by default.
	// Record this value to reproduce the output with a later release.
	EncoderCompatCurrent = EncoderCompatV1_20_3
)

// WithEncoderCompat makes the encoder produce the output of an earlier release.
// 0 selects EncoderCompatCurrent. Values above EncoderCompatCurrent are rejected.
// Cannot be changed with ResetWithOptions.
func WithEncoderCompat(c EncoderCompat) EOption {
	return func(o *encoderOptions) error {
		if c > EncoderCompatCurrent {
			return fmt.Errorf("unknown encoder compat %d, newest is %d", c, EncoderCompatCurrent)
		}
		if o.resetOpt && c.resolve() != o.compat.resolve() {
			return errors.New("WithEncoderCompat cannot be changed on Reset")
		}
		o.compat = c
		return nil
	}
}

// resolve maps 0 to EncoderCompatCurrent.
func (c EncoderCompat) resolve() EncoderCompat {
	if c == 0 {
		return EncoderCompatCurrent
	}
	return c
}

// older reports whether c selects the output of a release before n.
func (c EncoderCompat) older(n EncoderCompat) bool {
	return c.resolve() < n
}

// encoder returns an encoder with the selected options.
// Every encoder must take the compat level: a new upstream encoder that does not embed
// fastBase panics here, so tests fail instead of the level being ignored.
func (o encoderOptions) encoder() encoder {
	enc := o.encoderNoCompat()
	enc.(interface{ setCompat(EncoderCompat) }).setCompat(o.compat)
	return enc
}

// setCompat sets the level before first use; blocks made later copy it (see fastBase).
func (e *fastBase) setCompat(c EncoderCompat) {
	e.compat = c
}
