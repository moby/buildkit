package shell

import (
	"encoding/json"
	"io"

	"github.com/libp2p/go-libp2p-core/peer"
	mbase "github.com/multiformats/go-multibase"
)

// Message is a pubsub message.
type Message struct {
	From     peer.ID
	Data     []byte
	Seqno    []byte
	TopicIDs []string
}

// PubSubSubscription allow you to receive pubsub records that where published on the network.
type PubSubSubscription struct {
	resp io.Closer
	dec  *json.Decoder
}

func newPubSubSubscription(resp io.ReadCloser) *PubSubSubscription {
	return &PubSubSubscription{
		resp: resp,
		dec:  json.NewDecoder(resp),
	}
}

// Next waits for the next record and returns that.
func (s *PubSubSubscription) Next() (*Message, error) {
	var r struct {
		From     string   `json:"from,omitempty"`
		Data     string   `json:"data,omitempty"`
		Seqno    string   `json:"seqno,omitempty"`
		TopicIDs []string `json:"topicIDs,omitempty"`
	}

	err := s.dec.Decode(&r)
	if err != nil {
		return nil, err
	}

	// fields are wrapped in multibase when sent over HTTP RPC
	// and need to be decoded (https://github.com/ipfs/go-ipfs/pull/8183)
	from, err := peer.Decode(r.From)
	if err != nil {
		return nil, err
	}
	_, data, err := mbase.Decode(r.Data)
	if err != nil {
		return nil, err
	}
	_, seqno, err := mbase.Decode(r.Seqno)
	if err != nil {
		return nil, err
	}
	topics := make([]string, len(r.TopicIDs))
	for _, mbtopic := range r.TopicIDs {
		_, topic, err := mbase.Decode(mbtopic)
		if err != nil {
			return nil, err
		}
		topics = append(topics, string(topic))
	}
	return &Message{
		From:     from,
		Data:     data,
		Seqno:    seqno,
		TopicIDs: topics,
	}, nil
}

// Cancel cancels the given subscription.
func (s *PubSubSubscription) Cancel() error {
	return s.resp.Close()
}
