package pb

import proto "google.golang.org/protobuf/proto"

func (m *Definition) IsNil() bool {
	return m == nil || m.Metadata == nil
}

func (m *Definition) Marshal() ([]byte, error) {
	return proto.Marshal(m)
}

func (m *Definition) Unmarshal(dAtA []byte) error {
	return proto.Unmarshal(dAtA, m)
}

func (m *Op) Marshal() ([]byte, error) {
	return proto.Marshal(m)
}

func (m *Op) Unmarshal(dAtA []byte) error {
	return proto.Unmarshal(dAtA, m)
}
