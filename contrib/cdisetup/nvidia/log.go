package nvidia

import (
	"log"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
)

func newStream(pw progress.Writer, stream int, dgst digest.Digest) *streamWriter {
	return &streamWriter{
		pw:     pw,
		stream: stream,
		dgst:   dgst,
	}
}

type streamWriter struct {
	pw     progress.Writer
	stream int
	dgst   digest.Digest
}

func (sw *streamWriter) Write(dt []byte) (int, error) {
	err := sw.pw.Write(identity.NewID(), client.VertexLog{
		Vertex: sw.dgst,
		Stream: sw.stream,
		Data:   dt,
	})
	if err != nil {
		return 0, err
	}
	log.Printf("%d %s", sw.stream, dt)
	return len(dt), nil
}
