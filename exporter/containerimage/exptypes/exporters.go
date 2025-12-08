package exptypes

type ExporterTarget string

const (
	ExporterTargetUnknown   ExporterTarget = ""
	ExporterTargetNone      ExporterTarget = "none"
	ExporterTargetFile      ExporterTarget = "file"
	ExporterTargetDirectory ExporterTarget = "directory"
	ExporterTargetStore     ExporterTarget = "store"
)

func (t ExporterTarget) String() string {
	return string(t)
}
