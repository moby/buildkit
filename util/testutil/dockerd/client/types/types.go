package types

type Ping struct{}

// CloseWriter is an interface that implements structs
// that close input streams to prevent from writing.
type CloseWriter interface {
	CloseWrite() error
}

type ErrorResponse struct {
	Message string `json:"message"`
}
