package attestation

const (
	MediaTypeAttestationLayer     = "application/vnd.in-toto+json"
	MediaTypeOCIAttestationConfig = "application/vnd.docker.attestation.config.v1+json"

	DockerAnnotationReferenceType        = "vnd.docker.reference.type"
	DockerAnnotationReferenceDigest      = "vnd.docker.reference.digest"
	DockerAnnotationReferenceDescription = "vnd.docker.reference.description"

	DockerAnnotationReferenceTypeDefault = "attestation-manifest"
)
