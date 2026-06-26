package exptypes

const (
	ExporterEpochKey = "source.date.epoch"
)

type ExporterOptKey string

// Option keys supported by all exporters:

// OptKeySourceDateEpoch clamps produced timestamps. For more information
// see the [SOURCE_DATE_EPOCH] specification. This option is supported by
// all exporters.
//
// Value: int (number of seconds since Unix epoch).
//
// [SOURCE_DATE_EPOCH]: https://reproducible-builds.org/docs/source-date-epoch/
const OptKeySourceDateEpoch ExporterOptKey = "source-date-epoch"
