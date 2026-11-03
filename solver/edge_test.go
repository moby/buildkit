package solver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// cancelOnRead cancels the request the first time the edge reads it, after
// respondToIncoming has started deciding on its incoming requests.
type cancelOnRead struct {
	pipeSender
	cancel func()
	reads  int
}

func (p *cancelOnRead) Request() pipeRequest {
	r := p.pipeSender.Request()
	p.reads++
	if p.reads == 1 {
		p.cancel()
	}
	return r
}

func TestCancelDuringRespondKeepsIncomingUntilOutgoingDrains(t *testing.T) {
	p := newPipe(pipeRequest{Payload: &edgeRequest{desiredState: edgeStatusComplete}})
	req := &cancelOnRead{pipeSender: p.Sender, cancel: p.Receiver.Cancel}
	e := &edge{hasActiveOutgoing: true}

	e.respondToIncoming([]pipeSender{req}, nil)
	require.False(t, p.Sender.Status().Completed, "last incoming request completed with active outgoing")

	// The next dispatch sees the cancel and asks outgoing work to drain.
	_, done := e.respondToIncoming([]pipeSender{req}, nil)
	require.True(t, done)
	require.False(t, p.Sender.Status().Completed)

	e.hasActiveOutgoing = false
	e.respondToIncoming([]pipeSender{req}, nil)
	require.True(t, p.Sender.Status().Completed)
}
