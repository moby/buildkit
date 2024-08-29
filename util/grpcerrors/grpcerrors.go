package grpcerrors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	rpc "github.com/gogo/googleapis/google/rpc"
	gogotypes "github.com/gogo/protobuf/types"
	"github.com/golang/protobuf/proto" //nolint:staticcheck
	"github.com/moby/buildkit/errdefs"
	"github.com/moby/buildkit/util/stack"
	pkgerrors "github.com/pkg/errors"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type TypedError interface {
	ToProto() TypedErrorProto
}

type TypedErrorProto interface {
	proto.Message
	WrapError(error) error
}

// ToGRPC will encode an error into a status.Status
// that can be deserialized to retain most of the original
// structure from the original error.
//
// This method uses typeurl to marshal the error and supports
// marshaling wrapped errors in further details.
func ToGRPC(ctx context.Context, err error) error {
	if err == nil || isGRPCError(err) {
		return err
	}

	p := &spb.Status{
		Code:    int32(Code(err)),
		Message: err.Error(),
	}

	// Unwrap the withCode wrapper if we find it to
	// prevent wrapping the code multiple times.
	if e, ok := err.(*withCode); ok {
		err = e.Unwrap()
	}

	detail := &errdefs.Error{
		Message: p.Message,
	}
	withDetails(detail, err)

	if any, _ := typeurl.MarshalAny(detail); any != nil {
		p.Details = append(p.Details, &anypb.Any{
			TypeUrl: any.GetTypeUrl(),
			Value:   any.GetValue(),
		})
	}

	// This only applies to the top-level and is for
	// compatibility with older clients. The new
	// types that are marshaled will be ignored and
	// the new one will ignore the old types when
	// they aren't wrapped in a Status so marshal
	// the TypedError and stack traces as expected
	// by the old format.
	//
	// This can probably be removed after a few releases.
	for _, st := range stack.Traces(err) {
		if d := marshalTypedError(st); d != nil {
			p.Details = append(p.Details, &anypb.Any{
				TypeUrl: d.GetTypeUrl(),
				Value:   d.GetValue(),
			})
		}
	}

	for cur := err; cur != nil; {
		if typed, ok := cur.(TypedError); ok {
			if d := marshalTypedError(typed.ToProto()); d != nil {
				p.Details = append(p.Details, &anypb.Any{
					TypeUrl: d.GetTypeUrl(),
					Value:   d.GetValue(),
				})
			}
		}

		// Intentionally ignore Unwrap() []error since
		// the old serialization scheme didn't support it.
		switch e := cur.(type) {
		case interface{ Unwrap() error }:
			cur = e.Unwrap()
		default:
			cur = nil
		}
	}

	st := status.FromProto(p)
	return st.Err()
}

func isGRPCError(err error) bool {
	_, ok := status.FromError(err)
	return ok
}

func withDetails(p *errdefs.Error, err error) {
	any := marshalAny(err)

	// Record the details of the current message.
	p.Details = &anypb.Any{
		TypeUrl: any.GetTypeUrl(),
		Value:   any.GetValue(),
	}

	// Record any errors from unwrapping the current error.
	// We check both versions of Unwrap to get this correct.
	var errs []error
	switch err := err.(type) {
	case interface{ Unwrap() error }:
		if unwrapped := err.Unwrap(); unwrapped != nil {
			errs = []error{unwrapped}
		}
	case interface{ Unwrap() []error }:
		errs = err.Unwrap()
	}

	for _, err := range errs {
		detail := &errdefs.Error{
			Message: err.Error(),
		}
		withDetails(detail, err)
		p.Errors = append(p.Errors, detail)
	}
}

// marshalAny will marshal the error into a typeurl.Any.
// If this isn't possible, it will marshal it as an empty
// protobuf to ensure that it is non-nil.
func marshalAny(err error) (any typeurl.Any) {
	if typed, ok := err.(TypedError); ok {
		any = marshalTypedError(typed.ToProto())
	}
	if any == nil {
		any = marshalStacktrace(err)
	}
	if any == nil {
		// typeurl.MarshalAny only accepts a pointer and will
		// panic otherwise. Protect ourselves from that behavior.
		if t := reflect.TypeOf(err); t.Kind() == reflect.Ptr {
			any, _ = typeurl.MarshalAny(err)
		}
	}
	if any == nil {
		any, _ = anypb.New(&emptypb.Empty{})
	}
	return any
}

// marshalTypedError will marshal a TypedError
// into an Any protobuf with JSON marshaling.
//
// If this fails, it will return nil.
func marshalTypedError(p proto.Message) typeurl.Any {
	url, err := typeurl.TypeURL(p)
	if err != nil {
		return nil
	}

	// TODO: This isn't strictly correct, but changing
	// this at this point would be difficult with existing
	// serialization and backwards compatibility.
	//
	// The typeurl library has a bug in it where a protobuf
	// message will be serialized as protobuf but it will
	// attempt to deserialize it with JSON if it is registered
	// with typeurl.Register. All of our typed errors do that
	// so all of them encounter this bug. In order to avoid this
	// bug and maintain backwards compatibility, we force JSON
	// marshaling and are just really careful about making sure
	// to register the typeurl.
	dt, err := json.Marshal(p)
	if err != nil {
		return nil
	}

	return &anypb.Any{
		TypeUrl: url,
		Value:   dt,
	}
}

func marshalStacktrace(err error) typeurl.Any {
	var st *stack.Stack
	switch err := err.(type) {
	case interface{ StackTrace() pkgerrors.StackTrace }:
		st = stack.Convert(err.StackTrace())
	case interface{ StackTrace() *stack.Stack }:
		st = err.StackTrace()
	default:
		return nil
	}

	// stack.Stack is affected by the same bug as the other
	// typed errors so reuse that marshaling function to
	// marshal the stack trace.
	return marshalTypedError(st)
}

// Code will produce an error code from the error. It will
// iterate through the error chain in a depth-first search
// order unwrapping errors to determine the code.
//
// The following interfaces are supported in the given order:
// * github.com/moby/buildkit/errdefs.Internal
// * GRPCStatus() *google.golang.org/grpc/status.Status
// * github.com/containerd/errdefs
// * context.Canceled and context.DeadlineExceeded
//
// If none of the above types of errors exist in the chain,
// this will return the Unknown code. The nil value for an
// error will return OK.
func Code(err error) codes.Code {
	if err == nil {
		return codes.OK
	}

	// This is a stack so the last error is the
	// first out. In general, this slice should
	// remain a consistent size because most
	// errors are linear trees so it will keep
	// pushing and popping to the same memory
	// location. This prevents us from accumulating
	// an unnecessary stack. Even in the worst case
	// with a lot of joined errors this should
	// still perform better.
	unvisited := []error{err}
	for len(unvisited) > 0 {
		err, unvisited = pop(unvisited)

		switch err := err.(type) {
		case interface{ System() }:
			return codes.Internal
		case interface{ GRPCStatus() *status.Status }:
			st := err.GRPCStatus()
			if code := st.Code(); code != codes.OK && code != codes.Unknown {
				return code
			}
		case interface{ Code() codes.Code }:
			return err.Code()
		}

		switch err {
		case cerrdefs.ErrInvalidArgument:
			return codes.InvalidArgument
		case cerrdefs.ErrNotFound:
			return codes.NotFound
		case cerrdefs.ErrAlreadyExists:
			return codes.AlreadyExists
		case cerrdefs.ErrFailedPrecondition:
			return codes.FailedPrecondition
		case cerrdefs.ErrUnavailable:
			return codes.Unavailable
		case cerrdefs.ErrNotImplemented:
			return codes.Unimplemented
		case context.Canceled:
			return codes.Canceled
		case context.DeadlineExceeded:
			return codes.DeadlineExceeded
		}

		// Unwrap the error if this method is supported.
		switch err := err.(type) {
		case interface{ Unwrap() error }:
			if child := err.Unwrap(); child != nil {
				unvisited = push(unvisited, child)
			}
		case interface{ Unwrap() []error }:
			children := err.Unwrap()
			if len(children) == 0 {
				// Fast path although unlikely.
				continue
			}
			unvisited = push(unvisited, children...)
		}
	}
	return codes.Unknown
}

func pop[T any](s []T) (elem T, rest []T) {
	elem = s[len(s)-1]
	rest = s[:len(s)-1]
	return elem, rest
}

func push[T any](s []T, elems ...T) []T {
	if len(elems) == 1 {
		return append(s, elems...)
	}

	// If there are multiple elements, batch
	// add them and reverse the order so the first
	// element is the last in the array (FIFO).
	i := len(s)
	s = append(s, elems...)
	for j := len(s) - 1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}

func WrapCode(err error, code codes.Code) error {
	return &withCode{error: err, code: code}
}

func AsGRPCStatus(err error) (*status.Status, bool) {
	if err == nil {
		return nil, true
	}
	if se, ok := err.(interface {
		GRPCStatus() *status.Status
	}); ok {
		return se.GRPCStatus(), true
	}

	wrapped, ok := err.(interface {
		Unwrap() error
	})
	if ok {
		if err := wrapped.Unwrap(); err != nil {
			return AsGRPCStatus(err)
		}
	}

	return nil, false
}

func FromGRPC(err error) error {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	pb := st.Proto()

	// Iterate through the details looking for a known
	// serialization scheme. If none is found, attempt to
	// unmarshal as v1.
	for _, detail := range pb.Details {
		switch {
		case isV2(detail):
			if any, err := typeurl.UnmarshalAny(detail); err == nil {
				if detail, ok := any.(*errdefs.Error); ok {
					e := unmarshalV2(detail)
					if code := codes.Code(pb.Code); code != codes.OK && code != codes.Unknown {
						e = WrapCode(e, code)
					}
					return e
				}
			}
		}
	}
	return unmarshalV1(pb)
}

var v2TypeURL = sync.OnceValue(func() string {
	url, _ := typeurl.TypeURL((*errdefs.Error)(nil))
	return url
})

func isV2(any *anypb.Any) bool {
	return any.GetTypeUrl() == v2TypeURL()
}

func unmarshalV1(pb *spb.Status) (err error) {
	n := &spb.Status{
		Code:    pb.Code,
		Message: pb.Message,
	}

	details := make([]TypedErrorProto, 0, len(pb.Details))
	stacks := make([]*stack.Stack, 0, len(pb.Details))

	// details that we don't understand are copied as proto
	for _, d := range pb.Details {
		m, err := typeurl.UnmarshalAny(gogoAny(d))
		if err != nil {
			continue
		}

		switch v := m.(type) {
		case *stack.Stack:
			stacks = append(stacks, v)
		case TypedErrorProto:
			details = append(details, v)
		default:
			n.Details = append(n.Details, d)
		}
	}

	err = &grpcStatusError{st: status.FromProto(n)}

	for _, s := range stacks {
		if s != nil {
			err = stack.Wrap(err, s)
		}
	}

	for _, d := range details {
		err = d.WrapError(err)
	}

	if err != nil {
		stack.Helper()
	}

	return stack.Enable(err)
}

func ToRPCStatus(st *spb.Status) *rpc.Status {
	details := make([]*gogotypes.Any, len(st.Details))

	for i, d := range st.Details {
		details[i] = gogoAny(d)
	}

	return &rpc.Status{
		Code:    int32(st.Code),
		Message: st.Message,
		Details: details,
	}
}

type grpcStatusError struct {
	st *status.Status
}

func (e *grpcStatusError) Error() string {
	if e.st.Code() == codes.OK || e.st.Code() == codes.Unknown {
		return e.st.Message()
	}
	return e.st.Code().String() + ": " + e.st.Message()
}

func (e *grpcStatusError) GRPCStatus() *status.Status {
	return e.st
}

type withCode struct {
	code codes.Code
	error
}

func (e *withCode) Code() codes.Code {
	return e.code
}

func (e *withCode) Unwrap() error {
	return e.error
}

func gogoAny(in *anypb.Any) *gogotypes.Any {
	return &gogotypes.Any{
		TypeUrl: in.TypeUrl,
		Value:   in.Value,
	}
}

func unmarshalV2(p *errdefs.Error) (err error) {
	// Unmarshal the detail if it's included.
	if p.Details != nil {
		v, terr := typeurl.UnmarshalAny(p.Details)
		if terr == nil {
			switch verr := v.(type) {
			case TypedErrorProto:
				// Special case since this isn't an error. Wrap
				// the error we created in a special struct.
				err = &typedProtoError{
					TypedErrorProto: verr,
					msg:             p.Message,
				}
				err = verr.WrapError(err)
			case *stack.Stack:
				// Special case since stack doesn't support the error
				// interface.
				err = &stackError{
					msg:   p.Message,
					stack: verr,
				}
			case error:
				// Successfully unmarshaled the type as an error.
				// Use this instead.
				err = verr
			default:
				err = errors.New(p.Message)
			}
		}
	}

	// Unmarshal the wrapped errors.
	if len(p.Errors) > 0 {
		wrapped := make([]error, 0, len(p.Errors))
		for _, detail := range p.Errors {
			wrapped = append(wrapped, unmarshalV2(detail))
		}
		err = wrap(err, wrapped)
	}
	return err
}

// wrap will wrap errs within the parent error.
// If the parent supports errorWrapper, it will use that.
// Otherwise, it will generate the necessary structs
// to fit the structure.
func wrap(parent error, errs []error) error {
	switch len(errs) {
	case 0:
		// Weird. This should never be invoked.
		// The correct solution is to just return the
		// original error and not perform any wrapping.
		return parent
	case 1:
		// If one of the error wrappers is implemented, use that
		// to wrap the error.
		switch parent := parent.(type) {
		case interface{ WrapError(error) error }:
			return parent.WrapError(errs[0])
		case interface{ WrapError([]error) error }:
			return parent.WrapError(errs)
		}

		// Create a default wrapper that conforms to
		// the errors API. Since there is only one wrapped
		// error, we use a version that supports Unwrap() error
		// since there's more compatibility with that interface.
		return &wrapError{
			error: parent,
			err:   errs[0],
		}
	default:
		// Similar to a single error, look for a WrapError method that
		// takes in an error slice. We do not need to look for the
		// singular version of WrapError since that wouldn't be valid
		// anyway.
		if err, ok := parent.(interface{ WrapError([]error) error }); ok {
			return err.WrapError(errs)
		}

		return &wrapErrors{
			error: parent,
			errs:  errs,
		}
	}
}

type wrapError struct {
	error
	err error
}

func (e *wrapError) As(target interface{}) bool {
	return errors.As(e.error, target)
}

func (e *wrapError) Is(target error) bool {
	return errors.Is(e.error, target)
}

func (e *wrapError) Unwrap() error {
	return e.err
}

func (e *wrapError) Format(s fmt.State, verb rune) {
	formatError(e, s, verb)
}

type wrapErrors struct {
	error
	errs []error
}

func (e *wrapErrors) As(target interface{}) bool {
	return errors.As(e.error, target)
}

func (e *wrapErrors) Is(target error) bool {
	return errors.Is(e.error, target)
}

func (e *wrapErrors) Unwrap() []error {
	return e.errs
}

func (e *wrapErrors) Format(s fmt.State, verb rune) {
	formatError(e, s, verb)
}

type stackError struct {
	msg   string
	stack *stack.Stack
}

func (e *stackError) Error() string {
	return e.msg
}

func (e *stackError) StackTrace() *stack.Stack {
	return e.stack
}

func (e *stackError) WrapError(err error) error {
	// If this stack error wraps another error,
	// use the proper implementation that holds
	// the wrapped error rather than the message.
	return stack.Wrap(err, e.stack)
}

func (e *stackError) Format(s fmt.State, verb rune) {
	formatError(e, s, verb)
}

// typedProtoError is a special error
// that wraps a TypedErrorProto to allow for
// the WrapError logic to work.
type typedProtoError struct {
	TypedErrorProto
	msg string
}

func (e *typedProtoError) Error() string {
	return e.msg
}

func init() {
	typeurl.Register((*header)(nil), "github.com/moby/buildkit", "errdefs.Header+json")
}

type header struct {
	Version int `json:"version"`
}

func formatError(err error, s fmt.State, verb rune) {
	formatter := stack.Formatter(err)
	formatter.Format(s, verb)
}
