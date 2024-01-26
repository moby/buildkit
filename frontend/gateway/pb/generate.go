package moby_buildkit_v1_frontend //nolint:revive

//go:generate protoc -I=. -I=../../../vendor/ -I=../../../../../../ --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative:. gateway.proto

const (
	AttestationKindInToto = AttestationKind_InToto
	AttestationKindBundle = AttestationKind_Bundle

	InTotoSubjectKindSelf = InTotoSubjectKind_Self
	InTotoSubjectKindRaw  = InTotoSubjectKind_Raw
)
