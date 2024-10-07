// Package proto implements a custom encoding for grpc.
//
// The codec registered by this package has an important
// difference from the default codec. Unlike the default codec,
// it does not reset the input message when unmarshaling.
//
// This primarily impacts stream service calls. If you want
// to reuse a message and need it to be reset before calling
// RecvMsg, invoke the appropriate Reset method or proto.Reset
// before invoking RecvMsg or use a zero initialized message.
package proto

import (
	"github.com/pkg/errors"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"

	// It is important that this import is included so
	// we can overwrite the default codec with the same name.
	grpcproto "google.golang.org/grpc/encoding/proto"
)

func init() {
	encoding.RegisterCodecV2(codec{})
}

type codec struct{}

func (codec) Marshal(m any) (data mem.BufferSlice, err error) {
	switch m := m.(type) {
	case vtprotoMessage:
		return marshalVT(data, m)
	case protoadapt.MessageV2:
		return marshal(data, m)
	case protoadapt.MessageV1:
		m2 := protoadapt.MessageV2Of(m)
		return marshal(data, m2)
	default:
		return nil, errors.Errorf("failed to marshal, message is %T, want proto.Message", m)
	}
}

func (codec) Unmarshal(data mem.BufferSlice, m any) error {
	switch m := m.(type) {
	case vtprotoMessage:
		return unmarshalVT(data, m)
	case protoadapt.MessageV2:
		return unmarshal(data, m)
	case protoadapt.MessageV1:
		m2 := protoadapt.MessageV2Of(m)
		return unmarshal(data, m2)
	default:
		return errors.Errorf("failed to unmarshal, message is %T, want proto.Message", m)
	}
}

func (codec) Name() string {
	return grpcproto.Name
}

type vtprotoMessage interface {
	MarshalToSizedBufferVT(data []byte) (int, error)
	SizeVT() int
	UnmarshalVT(data []byte) error
}

func marshalVT(data mem.BufferSlice, m vtprotoMessage) (mem.BufferSlice, error) {
	size := m.SizeVT()
	if mem.IsBelowBufferPoolingThreshold(size) {
		buf := make([]byte, size)
		if _, err := m.MarshalToSizedBufferVT(buf); err != nil {
			return nil, err
		}
		data = append(data, mem.SliceBuffer(buf))
	} else {
		pool := mem.DefaultBufferPool()
		buf := pool.Get(size)
		if _, err := m.MarshalToSizedBufferVT((*buf)[:size]); err != nil {
			pool.Put(buf)
			return nil, err
		}
		data = append(data, mem.NewBuffer(buf, pool))
	}
	return data, nil
}

func unmarshalVT(data mem.BufferSlice, m vtprotoMessage) error {
	buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
	defer buf.Free()

	return m.UnmarshalVT(buf.ReadOnlyData())
}

func marshal(data mem.BufferSlice, m proto.Message) (mem.BufferSlice, error) {
	size := proto.Size(m)
	if mem.IsBelowBufferPoolingThreshold(size) {
		buf, err := proto.Marshal(m)
		if err != nil {
			return nil, err
		}
		data = append(data, mem.SliceBuffer(buf))
	} else {
		pool := mem.DefaultBufferPool()
		buf := pool.Get(size)
		if _, err := (proto.MarshalOptions{}).MarshalAppend((*buf)[:0], m); err != nil {
			pool.Put(buf)
			return nil, err
		}
		data = append(data, mem.NewBuffer(buf, pool))
	}
	return data, nil
}

func unmarshal(data mem.BufferSlice, m proto.Message) error {
	buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
	defer buf.Free()

	// We use merge true here for consistency with the vtproto implementation,
	// but this won't impact performance most of the time. This is because
	// the buffer that's most likely to impact performance is a bytes message
	// and the default codec for protobuf will never reuse a buffer
	// from the existing message.
	//
	// We don't want any surprises between the vtproto implementation and this
	// implementation so we enable merge in case it causes a visible behavior
	// difference.
	return proto.UnmarshalOptions{Merge: true}.Unmarshal(buf.ReadOnlyData(), m)
}
