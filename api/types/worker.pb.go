// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: worker.proto

package moby_buildkit_v1_types

import (
	fmt "fmt"
	_ "github.com/gogo/protobuf/gogoproto"
	proto "github.com/gogo/protobuf/proto"
	pb "github.com/moby/buildkit/solver/pb"
	io "io"
	math "math"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.GoGoProtoPackageIsVersion2 // please upgrade the proto package

type WorkerRecord struct {
	ID                   string            `protobuf:"bytes,1,opt,name=ID,proto3" json:"ID,omitempty"`
	Labels               map[string]string `protobuf:"bytes,2,rep,name=Labels,proto3" json:"Labels,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
	Platforms            []pb.Platform     `protobuf:"bytes,3,rep,name=platforms,proto3" json:"platforms"`
	GCPolicy             []*GCPolicy       `protobuf:"bytes,4,rep,name=GCPolicy,proto3" json:"GCPolicy,omitempty"`
	XXX_NoUnkeyedLiteral struct{}          `json:"-"`
	XXX_unrecognized     []byte            `json:"-"`
	XXX_sizecache        int32             `json:"-"`
}

func (m *WorkerRecord) Reset()         { *m = WorkerRecord{} }
func (m *WorkerRecord) String() string { return proto.CompactTextString(m) }
func (*WorkerRecord) ProtoMessage()    {}
func (*WorkerRecord) Descriptor() ([]byte, []int) {
	return fileDescriptor_e4ff6184b07e587a, []int{0}
}
func (m *WorkerRecord) XXX_Unmarshal(b []byte) error {
	return m.Unmarshal(b)
}
func (m *WorkerRecord) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	if deterministic {
		return xxx_messageInfo_WorkerRecord.Marshal(b, m, deterministic)
	} else {
		b = b[:cap(b)]
		n, err := m.MarshalTo(b)
		if err != nil {
			return nil, err
		}
		return b[:n], nil
	}
}
func (m *WorkerRecord) XXX_Merge(src proto.Message) {
	xxx_messageInfo_WorkerRecord.Merge(m, src)
}
func (m *WorkerRecord) XXX_Size() int {
	return m.Size()
}
func (m *WorkerRecord) XXX_DiscardUnknown() {
	xxx_messageInfo_WorkerRecord.DiscardUnknown(m)
}

var xxx_messageInfo_WorkerRecord proto.InternalMessageInfo

func (m *WorkerRecord) GetID() string {
	if m != nil {
		return m.ID
	}
	return ""
}

func (m *WorkerRecord) GetLabels() map[string]string {
	if m != nil {
		return m.Labels
	}
	return nil
}

func (m *WorkerRecord) GetPlatforms() []pb.Platform {
	if m != nil {
		return m.Platforms
	}
	return nil
}

func (m *WorkerRecord) GetGCPolicy() []*GCPolicy {
	if m != nil {
		return m.GCPolicy
	}
	return nil
}

type GCPolicy struct {
	All                  bool     `protobuf:"varint,1,opt,name=all,proto3" json:"all,omitempty"`
	KeepDuration         int64    `protobuf:"varint,2,opt,name=keepDuration,proto3" json:"keepDuration,omitempty"`
	KeepBytes            int64    `protobuf:"varint,3,opt,name=keepBytes,proto3" json:"keepBytes,omitempty"`
	Filters              []string `protobuf:"bytes,4,rep,name=filters,proto3" json:"filters,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *GCPolicy) Reset()         { *m = GCPolicy{} }
func (m *GCPolicy) String() string { return proto.CompactTextString(m) }
func (*GCPolicy) ProtoMessage()    {}
func (*GCPolicy) Descriptor() ([]byte, []int) {
	return fileDescriptor_e4ff6184b07e587a, []int{1}
}
func (m *GCPolicy) XXX_Unmarshal(b []byte) error {
	return m.Unmarshal(b)
}
func (m *GCPolicy) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	if deterministic {
		return xxx_messageInfo_GCPolicy.Marshal(b, m, deterministic)
	} else {
		b = b[:cap(b)]
		n, err := m.MarshalTo(b)
		if err != nil {
			return nil, err
		}
		return b[:n], nil
	}
}
func (m *GCPolicy) XXX_Merge(src proto.Message) {
	xxx_messageInfo_GCPolicy.Merge(m, src)
}
func (m *GCPolicy) XXX_Size() int {
	return m.Size()
}
func (m *GCPolicy) XXX_DiscardUnknown() {
	xxx_messageInfo_GCPolicy.DiscardUnknown(m)
}

var xxx_messageInfo_GCPolicy proto.InternalMessageInfo

func (m *GCPolicy) GetAll() bool {
	if m != nil {
		return m.All
	}
	return false
}

func (m *GCPolicy) GetKeepDuration() int64 {
	if m != nil {
		return m.KeepDuration
	}
	return 0
}

func (m *GCPolicy) GetKeepBytes() int64 {
	if m != nil {
		return m.KeepBytes
	}
	return 0
}

func (m *GCPolicy) GetFilters() []string {
	if m != nil {
		return m.Filters
	}
	return nil
}

func init() {
	proto.RegisterType((*WorkerRecord)(nil), "moby.buildkit.v1.types.WorkerRecord")
	proto.RegisterMapType((map[string]string)(nil), "moby.buildkit.v1.types.WorkerRecord.LabelsEntry")
	proto.RegisterType((*GCPolicy)(nil), "moby.buildkit.v1.types.GCPolicy")
}

func init() { proto.RegisterFile("worker.proto", fileDescriptor_e4ff6184b07e587a) }

var fileDescriptor_e4ff6184b07e587a = []byte{
	// 355 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0x74, 0x91, 0xc1, 0x4e, 0xea, 0x40,
	0x14, 0x86, 0x6f, 0x5b, 0x2e, 0x97, 0x0e, 0xcd, 0x8d, 0x99, 0x18, 0xd3, 0x10, 0x83, 0x84, 0x15,
	0x0b, 0x9d, 0xa2, 0x6e, 0xd4, 0xb8, 0x42, 0x8c, 0x92, 0xb8, 0x20, 0xb3, 0x71, 0xdd, 0x81, 0x01,
	0x9b, 0x0e, 0x9c, 0xc9, 0x74, 0x8a, 0xf6, 0x39, 0x7c, 0x29, 0x96, 0x3e, 0x81, 0x31, 0x3c, 0x89,
	0x99, 0x29, 0x08, 0x26, 0xba, 0x3b, 0xff, 0x9f, 0xff, 0xfb, 0xe7, 0x9c, 0x0c, 0x0a, 0x9e, 0x41,
	0xa5, 0x5c, 0x11, 0xa9, 0x40, 0x03, 0x3e, 0x98, 0x01, 0x2b, 0x08, 0xcb, 0x13, 0x31, 0x4e, 0x13,
	0x4d, 0x16, 0xa7, 0x44, 0x17, 0x92, 0x67, 0x8d, 0x93, 0x69, 0xa2, 0x9f, 0x72, 0x46, 0x46, 0x30,
	0x8b, 0xa6, 0x30, 0x85, 0xc8, 0xc6, 0x59, 0x3e, 0xb1, 0xca, 0x0a, 0x3b, 0x95, 0x35, 0x8d, 0xe3,
	0x9d, 0xb8, 0x69, 0x8c, 0x36, 0x8d, 0x51, 0x06, 0x62, 0xc1, 0x55, 0x24, 0x59, 0x04, 0x32, 0x2b,
	0xd3, 0xed, 0x57, 0x17, 0x05, 0x8f, 0x76, 0x0b, 0xca, 0x47, 0xa0, 0xc6, 0xf8, 0x3f, 0x72, 0x07,
	0xfd, 0xd0, 0x69, 0x39, 0x1d, 0x9f, 0xba, 0x83, 0x3e, 0xbe, 0x47, 0xd5, 0x87, 0x98, 0x71, 0x91,
	0x85, 0x6e, 0xcb, 0xeb, 0xd4, 0xcf, 0xba, 0xe4, 0xe7, 0x35, 0xc9, 0x6e, 0x0b, 0x29, 0x91, 0xdb,
	0xb9, 0x56, 0x05, 0x5d, 0xf3, 0xb8, 0x8b, 0x7c, 0x29, 0x62, 0x3d, 0x01, 0x35, 0xcb, 0x42, 0xcf,
	0x96, 0x05, 0x44, 0x32, 0x32, 0x5c, 0x9b, 0xbd, 0xca, 0xf2, 0xfd, 0xe8, 0x0f, 0xdd, 0x86, 0xf0,
	0x35, 0xaa, 0xdd, 0xdd, 0x0c, 0x41, 0x24, 0xa3, 0x22, 0xac, 0x58, 0xa0, 0xf5, 0xdb, 0xeb, 0x9b,
	0x1c, 0xfd, 0x22, 0x1a, 0x97, 0xa8, 0xbe, 0xb3, 0x06, 0xde, 0x43, 0x5e, 0xca, 0x8b, 0xf5, 0x65,
	0x66, 0xc4, 0xfb, 0xe8, 0xef, 0x22, 0x16, 0x39, 0x0f, 0x5d, 0xeb, 0x95, 0xe2, 0xca, 0xbd, 0x70,
	0xda, 0x2f, 0xdb, 0x87, 0x0d, 0x17, 0x0b, 0x61, 0xb9, 0x1a, 0x35, 0x23, 0x6e, 0xa3, 0x20, 0xe5,
	0x5c, 0xf6, 0x73, 0x15, 0xeb, 0x04, 0xe6, 0x16, 0xf7, 0xe8, 0x37, 0x0f, 0x1f, 0x22, 0xdf, 0xe8,
	0x5e, 0xa1, 0xb9, 0x39, 0xd6, 0x04, 0xb6, 0x06, 0x0e, 0xd1, 0xbf, 0x49, 0x22, 0x34, 0x57, 0x99,
	0xbd, 0xcb, 0xa7, 0x1b, 0xd9, 0x0b, 0x96, 0xab, 0xa6, 0xf3, 0xb6, 0x6a, 0x3a, 0x1f, 0xab, 0xa6,
	0xc3, 0xaa, 0xf6, 0x93, 0xce, 0x3f, 0x03, 0x00, 0x00, 0xff, 0xff, 0xfc, 0x79, 0x52, 0x6a, 0x29,
	0x02, 0x00, 0x00,
}

func (m *WorkerRecord) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalTo(dAtA)
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *WorkerRecord) MarshalTo(dAtA []byte) (int, error) {
	var i int
	_ = i
	var l int
	_ = l
	if len(m.ID) > 0 {
		dAtA[i] = 0xa
		i++
		i = encodeVarintWorker(dAtA, i, uint64(len(m.ID)))
		i += copy(dAtA[i:], m.ID)
	}
	if len(m.Labels) > 0 {
		for k, _ := range m.Labels {
			dAtA[i] = 0x12
			i++
			v := m.Labels[k]
			mapSize := 1 + len(k) + sovWorker(uint64(len(k))) + 1 + len(v) + sovWorker(uint64(len(v)))
			i = encodeVarintWorker(dAtA, i, uint64(mapSize))
			dAtA[i] = 0xa
			i++
			i = encodeVarintWorker(dAtA, i, uint64(len(k)))
			i += copy(dAtA[i:], k)
			dAtA[i] = 0x12
			i++
			i = encodeVarintWorker(dAtA, i, uint64(len(v)))
			i += copy(dAtA[i:], v)
		}
	}
	if len(m.Platforms) > 0 {
		for _, msg := range m.Platforms {
			dAtA[i] = 0x1a
			i++
			i = encodeVarintWorker(dAtA, i, uint64(msg.Size()))
			n, err := msg.MarshalTo(dAtA[i:])
			if err != nil {
				return 0, err
			}
			i += n
		}
	}
	if len(m.GCPolicy) > 0 {
		for _, msg := range m.GCPolicy {
			dAtA[i] = 0x22
			i++
			i = encodeVarintWorker(dAtA, i, uint64(msg.Size()))
			n, err := msg.MarshalTo(dAtA[i:])
			if err != nil {
				return 0, err
			}
			i += n
		}
	}
	if m.XXX_unrecognized != nil {
		i += copy(dAtA[i:], m.XXX_unrecognized)
	}
	return i, nil
}

func (m *GCPolicy) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalTo(dAtA)
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *GCPolicy) MarshalTo(dAtA []byte) (int, error) {
	var i int
	_ = i
	var l int
	_ = l
	if m.All {
		dAtA[i] = 0x8
		i++
		if m.All {
			dAtA[i] = 1
		} else {
			dAtA[i] = 0
		}
		i++
	}
	if m.KeepDuration != 0 {
		dAtA[i] = 0x10
		i++
		i = encodeVarintWorker(dAtA, i, uint64(m.KeepDuration))
	}
	if m.KeepBytes != 0 {
		dAtA[i] = 0x18
		i++
		i = encodeVarintWorker(dAtA, i, uint64(m.KeepBytes))
	}
	if len(m.Filters) > 0 {
		for _, s := range m.Filters {
			dAtA[i] = 0x22
			i++
			l = len(s)
			for l >= 1<<7 {
				dAtA[i] = uint8(uint64(l)&0x7f | 0x80)
				l >>= 7
				i++
			}
			dAtA[i] = uint8(l)
			i++
			i += copy(dAtA[i:], s)
		}
	}
	if m.XXX_unrecognized != nil {
		i += copy(dAtA[i:], m.XXX_unrecognized)
	}
	return i, nil
}

func encodeVarintWorker(dAtA []byte, offset int, v uint64) int {
	for v >= 1<<7 {
		dAtA[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dAtA[offset] = uint8(v)
	return offset + 1
}
func (m *WorkerRecord) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.ID)
	if l > 0 {
		n += 1 + l + sovWorker(uint64(l))
	}
	if len(m.Labels) > 0 {
		for k, v := range m.Labels {
			_ = k
			_ = v
			mapEntrySize := 1 + len(k) + sovWorker(uint64(len(k))) + 1 + len(v) + sovWorker(uint64(len(v)))
			n += mapEntrySize + 1 + sovWorker(uint64(mapEntrySize))
		}
	}
	if len(m.Platforms) > 0 {
		for _, e := range m.Platforms {
			l = e.Size()
			n += 1 + l + sovWorker(uint64(l))
		}
	}
	if len(m.GCPolicy) > 0 {
		for _, e := range m.GCPolicy {
			l = e.Size()
			n += 1 + l + sovWorker(uint64(l))
		}
	}
	if m.XXX_unrecognized != nil {
		n += len(m.XXX_unrecognized)
	}
	return n
}

func (m *GCPolicy) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	if m.All {
		n += 2
	}
	if m.KeepDuration != 0 {
		n += 1 + sovWorker(uint64(m.KeepDuration))
	}
	if m.KeepBytes != 0 {
		n += 1 + sovWorker(uint64(m.KeepBytes))
	}
	if len(m.Filters) > 0 {
		for _, s := range m.Filters {
			l = len(s)
			n += 1 + l + sovWorker(uint64(l))
		}
	}
	if m.XXX_unrecognized != nil {
		n += len(m.XXX_unrecognized)
	}
	return n
}

func sovWorker(x uint64) (n int) {
	for {
		n++
		x >>= 7
		if x == 0 {
			break
		}
	}
	return n
}
func sozWorker(x uint64) (n int) {
	return sovWorker(uint64((x << 1) ^ uint64((int64(x) >> 63))))
}
func (m *WorkerRecord) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowWorker
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: WorkerRecord: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: WorkerRecord: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field ID", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthWorker
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthWorker
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.ID = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Labels", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthWorker
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthWorker
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			if m.Labels == nil {
				m.Labels = make(map[string]string)
			}
			var mapkey string
			var mapvalue string
			for iNdEx < postIndex {
				entryPreIndex := iNdEx
				var wire uint64
				for shift := uint(0); ; shift += 7 {
					if shift >= 64 {
						return ErrIntOverflowWorker
					}
					if iNdEx >= l {
						return io.ErrUnexpectedEOF
					}
					b := dAtA[iNdEx]
					iNdEx++
					wire |= uint64(b&0x7F) << shift
					if b < 0x80 {
						break
					}
				}
				fieldNum := int32(wire >> 3)
				if fieldNum == 1 {
					var stringLenmapkey uint64
					for shift := uint(0); ; shift += 7 {
						if shift >= 64 {
							return ErrIntOverflowWorker
						}
						if iNdEx >= l {
							return io.ErrUnexpectedEOF
						}
						b := dAtA[iNdEx]
						iNdEx++
						stringLenmapkey |= uint64(b&0x7F) << shift
						if b < 0x80 {
							break
						}
					}
					intStringLenmapkey := int(stringLenmapkey)
					if intStringLenmapkey < 0 {
						return ErrInvalidLengthWorker
					}
					postStringIndexmapkey := iNdEx + intStringLenmapkey
					if postStringIndexmapkey < 0 {
						return ErrInvalidLengthWorker
					}
					if postStringIndexmapkey > l {
						return io.ErrUnexpectedEOF
					}
					mapkey = string(dAtA[iNdEx:postStringIndexmapkey])
					iNdEx = postStringIndexmapkey
				} else if fieldNum == 2 {
					var stringLenmapvalue uint64
					for shift := uint(0); ; shift += 7 {
						if shift >= 64 {
							return ErrIntOverflowWorker
						}
						if iNdEx >= l {
							return io.ErrUnexpectedEOF
						}
						b := dAtA[iNdEx]
						iNdEx++
						stringLenmapvalue |= uint64(b&0x7F) << shift
						if b < 0x80 {
							break
						}
					}
					intStringLenmapvalue := int(stringLenmapvalue)
					if intStringLenmapvalue < 0 {
						return ErrInvalidLengthWorker
					}
					postStringIndexmapvalue := iNdEx + intStringLenmapvalue
					if postStringIndexmapvalue < 0 {
						return ErrInvalidLengthWorker
					}
					if postStringIndexmapvalue > l {
						return io.ErrUnexpectedEOF
					}
					mapvalue = string(dAtA[iNdEx:postStringIndexmapvalue])
					iNdEx = postStringIndexmapvalue
				} else {
					iNdEx = entryPreIndex
					skippy, err := skipWorker(dAtA[iNdEx:])
					if err != nil {
						return err
					}
					if skippy < 0 {
						return ErrInvalidLengthWorker
					}
					if (iNdEx + skippy) > postIndex {
						return io.ErrUnexpectedEOF
					}
					iNdEx += skippy
				}
			}
			m.Labels[mapkey] = mapvalue
			iNdEx = postIndex
		case 3:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Platforms", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthWorker
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthWorker
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Platforms = append(m.Platforms, pb.Platform{})
			if err := m.Platforms[len(m.Platforms)-1].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		case 4:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field GCPolicy", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthWorker
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthWorker
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.GCPolicy = append(m.GCPolicy, &GCPolicy{})
			if err := m.GCPolicy[len(m.GCPolicy)-1].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipWorker(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthWorker
			}
			if (iNdEx + skippy) < 0 {
				return ErrInvalidLengthWorker
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			m.XXX_unrecognized = append(m.XXX_unrecognized, dAtA[iNdEx:iNdEx+skippy]...)
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (m *GCPolicy) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowWorker
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: GCPolicy: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: GCPolicy: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field All", wireType)
			}
			var v int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				v |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			m.All = bool(v != 0)
		case 2:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field KeepDuration", wireType)
			}
			m.KeepDuration = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.KeepDuration |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 3:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field KeepBytes", wireType)
			}
			m.KeepBytes = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.KeepBytes |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 4:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Filters", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthWorker
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthWorker
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Filters = append(m.Filters, string(dAtA[iNdEx:postIndex]))
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipWorker(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthWorker
			}
			if (iNdEx + skippy) < 0 {
				return ErrInvalidLengthWorker
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			m.XXX_unrecognized = append(m.XXX_unrecognized, dAtA[iNdEx:iNdEx+skippy]...)
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func skipWorker(dAtA []byte) (n int, err error) {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, ErrIntOverflowWorker
			}
			if iNdEx >= l {
				return 0, io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		wireType := int(wire & 0x7)
		switch wireType {
		case 0:
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				iNdEx++
				if dAtA[iNdEx-1] < 0x80 {
					break
				}
			}
			return iNdEx, nil
		case 1:
			iNdEx += 8
			return iNdEx, nil
		case 2:
			var length int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowWorker
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				length |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if length < 0 {
				return 0, ErrInvalidLengthWorker
			}
			iNdEx += length
			if iNdEx < 0 {
				return 0, ErrInvalidLengthWorker
			}
			return iNdEx, nil
		case 3:
			for {
				var innerWire uint64
				var start int = iNdEx
				for shift := uint(0); ; shift += 7 {
					if shift >= 64 {
						return 0, ErrIntOverflowWorker
					}
					if iNdEx >= l {
						return 0, io.ErrUnexpectedEOF
					}
					b := dAtA[iNdEx]
					iNdEx++
					innerWire |= (uint64(b) & 0x7F) << shift
					if b < 0x80 {
						break
					}
				}
				innerWireType := int(innerWire & 0x7)
				if innerWireType == 4 {
					break
				}
				next, err := skipWorker(dAtA[start:])
				if err != nil {
					return 0, err
				}
				iNdEx = start + next
				if iNdEx < 0 {
					return 0, ErrInvalidLengthWorker
				}
			}
			return iNdEx, nil
		case 4:
			return iNdEx, nil
		case 5:
			iNdEx += 4
			return iNdEx, nil
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
	}
	panic("unreachable")
}

var (
	ErrInvalidLengthWorker = fmt.Errorf("proto: negative length found during unmarshaling")
	ErrIntOverflowWorker   = fmt.Errorf("proto: integer overflow")
)
