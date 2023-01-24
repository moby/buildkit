package shell

import (
	"context"
	"encoding/json"
	"io"
)

// Logger is used to handle incoming logs from the ipfs node
type Logger struct {
	resp io.ReadCloser
	dec  *json.Decoder
}

// Next is used to retrieve the next event from the logging system
func (l Logger) Next() (map[string]interface{}, error) {
	var out map[string]interface{}
	return out, l.dec.Decode(&out)
}

// Close is used to close our reader
func (l Logger) Close() error {
	return l.resp.Close()
}

// GetLogs is used to retrieve a parsable logger object
func (s *Shell) GetLogs(ctx context.Context) (Logger, error) {
	resp, err := s.Request("log/tail").Send(ctx)
	if err != nil {
		return Logger{}, err
	}
	if resp.Error != nil {
		resp.Output.Close()
		return Logger{}, resp.Error
	}
	return newLogger(resp.Output), nil
}

func newLogger(resp io.ReadCloser) Logger {
	return Logger{resp, json.NewDecoder(resp)}
}
