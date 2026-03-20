package exptypes

// ArtifactLayer describes a single layer in an OCI artifact.
// The frontend produces an array of these as JSON metadata, which the
// ArtifactProcessor passes to the OCI layout assembler container.
type ArtifactLayer struct {
	// Path is the root-relative file path inside the artifact input reference.
	Path        string            `json:"path"`
	MediaType   string            `json:"mediaType"`
	Annotations map[string]string `json:"annotations,omitempty"`
}
