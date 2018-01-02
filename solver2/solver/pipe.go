package solver

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"
)

type Channel struct {
	Signal    func()
	mu        sync.Mutex
	value     atomic.Value
	lastValue interface{}
}

func (c *Channel) Send(v interface{}) {
	c.value.Store(v)
	if c.Signal != nil {
		c.Signal()
	}
}

func (c *Channel) Receive() (interface{}, bool) {
	v := c.value.Load()
	if c.lastValue == v {
		return nil, false
	}
	c.lastValue = v
	return v, true
}

type Pipe struct {
	Writer       PipeWriter
	Reader       PipeReader
	SignalReader func()
	SignalWriter func()
}

type PipeRequest struct {
	Request  interface{} // Payload
	Canceled bool
}

type PipeWriter interface {
	Request() PipeRequest
	Update(v interface{})
	Finalize(v interface{}, err error)
	Status() PipeStatus
}

type PipeReader interface {
	Reload() bool
	Cancel()
	Status() PipeStatus
	Request() interface{}
}

type PipeStatus struct {
	Canceled  bool
	Completed bool
	Err       error
	Value     interface{}
}

func newFuncionPipe(f func(context.Context) (interface{}, error)) (*Pipe, func()) {
	p := NewPipe(PipeRequest{})

	ctx, cancel := context.WithCancel(context.TODO())

	p.SignalReader = func() {
		if req := p.Writer.Request(); req.Canceled {
			cancel()
		}
	}

	return p, func() {
		res, err := f(ctx)
		if err != nil {
			p.Writer.Finalize(nil, err)
			return
		}
		p.Writer.Finalize(res, nil)
	}
}

func NewPipe(req PipeRequest) *Pipe {
	cancelCh := &Channel{}
	roundTripCh := &Channel{}
	pw := &pipeWriter{
		req:         req,
		recvChannel: cancelCh,
		sendChannel: roundTripCh,
	}
	pr := &pipeReader{
		req:         req,
		recvChannel: roundTripCh,
		sendChannel: cancelCh,
	}

	p := &Pipe{
		Writer: pw,
		Reader: pr,
	}

	cancelCh.Signal = func() {
		v, ok := cancelCh.Receive()
		if ok {
			pw.setRequest(v.(PipeRequest))
		}
		if p.SignalReader != nil {
			p.SignalReader()
		}
	}

	roundTripCh.Signal = func() {
		if p.SignalWriter != nil {
			p.SignalWriter()
		}
	}

	return p
}

type pipeWriter struct {
	status      PipeStatus
	req         PipeRequest
	recvChannel *Channel
	sendChannel *Channel
	mu          sync.Mutex
}

func (pw *pipeWriter) Status() PipeStatus {
	return pw.status
}

func (pw *pipeWriter) Request() PipeRequest {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	return pw.req
}

func (pw *pipeWriter) setRequest(req PipeRequest) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.req = req
}

func (pw *pipeWriter) Update(v interface{}) {
	pw.status.Value = v
	pw.sendChannel.Send(pw.status)
}

func (pw *pipeWriter) Finalize(v interface{}, err error) {
	if v != nil {
		pw.status.Value = v
	}
	pw.status.Err = err
	pw.status.Completed = true
	if errors.Cause(err) == context.Canceled && pw.req.Canceled {
		pw.status.Canceled = true
	}
	pw.sendChannel.Send(pw.status)
}

type pipeReader struct {
	status      PipeStatus
	req         PipeRequest
	recvChannel *Channel
	sendChannel *Channel
}

func (pr *pipeReader) Request() interface{} {
	return pr.req.Request
}

func (pr *pipeReader) Reload() bool {
	v, ok := pr.recvChannel.Receive()
	if !ok {
		return false
	}
	pr.status = v.(PipeStatus)
	return true
}

func (pr *pipeReader) Cancel() {
	req := pr.req
	req.Canceled = true
	pr.sendChannel.Send(req)
}

func (pr *pipeReader) Status() PipeStatus {
	return pr.status
}
