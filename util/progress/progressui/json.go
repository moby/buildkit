package progressui

import (
	"time"

	digest "github.com/opencontainers/go-digest"
)

type Vertex struct {
	Digest   digest.Digest          `json:"digest,omitempty"`
	Inputs   []digest.Digest        `json:"inputs,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Started  *time.Time             `json:"started,omitempty"`
	Duration *float64               `json:"duration,omitempty"`
	Cached   bool                   `json:"cached,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Statuses []VertexStatus         `json:"statuses,omitempty"`
	Logs     map[string][]VertexLog `json:"logs,omitempty"`
	Warnings []VertexWarning        `json:"warnings,omitempty"`
}

type VertexStatus struct {
	ID       string     `json:"id"`
	Name     string     `json:"name,omitempty"`
	Total    int64      `json:"total,omitempty"`
	Started  *time.Time `json:"started,omitempty"`
	Duration *float64   `json:"duration,omitempty"`
}

type VertexLog struct {
	Timestamp float64 `json:"ts"`
	Message   string  `json:"msg"`
}

type VertexWarning struct {
	Level  int      `json:"level,omitempty"`
	Short  string   `json:"short,omitempty"`
	Detail []string `json:"detail,omitempty"`
	URL    string   `json:"url,omitempty"`
}
