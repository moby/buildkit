package moby_buildkit_v1 //nolint:revive,staticcheck

import (
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
)

func ExporterTargetFromPB(target ExporterTarget) exptypes.ExporterTarget {
	switch target {
	case ExporterTarget_UNKNOWN:
		return exptypes.ExporterTargetUnknown
	case ExporterTarget_NONE:
		return exptypes.ExporterTargetNone
	case ExporterTarget_FILE:
		return exptypes.ExporterTargetFile
	case ExporterTarget_DIRECTORY:
		return exptypes.ExporterTargetDirectory
	case ExporterTarget_STORE:
		return exptypes.ExporterTargetStore
	default:
		return exptypes.ExporterTargetUnknown
	}
}

func ExporterTargetToPB(target exptypes.ExporterTarget) ExporterTarget {
	switch target {
	case exptypes.ExporterTargetUnknown:
		return ExporterTarget_UNKNOWN
	case exptypes.ExporterTargetNone:
		return ExporterTarget_NONE
	case exptypes.ExporterTargetFile:
		return ExporterTarget_FILE
	case exptypes.ExporterTargetDirectory:
		return ExporterTarget_DIRECTORY
	case exptypes.ExporterTargetStore:
		return ExporterTarget_STORE
	default:
		return ExporterTarget_UNKNOWN
	}
}
