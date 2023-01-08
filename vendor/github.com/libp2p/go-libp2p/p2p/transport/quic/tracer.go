package libp2pquic

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/lucas-clemente/quic-go/logging"
	"github.com/lucas-clemente/quic-go/qlog"
)

var tracer logging.Tracer

func init() {
	tracers := []logging.Tracer{&metricsTracer{}}
	if qlogDir := os.Getenv("QLOGDIR"); len(qlogDir) > 0 {
		if qlogger := initQlogger(qlogDir); qlogger != nil {
			tracers = append(tracers, qlogger)
		}
	}
	tracer = logging.NewMultiplexedTracer(tracers...)
}

func initQlogger(qlogDir string) logging.Tracer {
	return qlog.NewTracer(func(role logging.Perspective, connID []byte) io.WriteCloser {
		// create the QLOGDIR, if it doesn't exist
		if err := os.MkdirAll(qlogDir, 0777); err != nil {
			log.Errorf("creating the QLOGDIR failed: %s", err)
			return nil
		}
		return newQlogger(qlogDir, role, connID)
	})
}

// The qlogger logs qlog events to a temporary file: .<name>.qlog.swp.
// When it is closed, it compresses the temporary file and saves it as <name>.qlog.zst.
// It is not possible to compress on the fly, as compression algorithms keep a lot of internal state,
// which can easily exhaust the host system's memory when running a few hundred QUIC connections in parallel.
type qlogger struct {
	f             *os.File // QLOGDIR/.log_xxx.qlog.swp
	filename      string   // QLOGDIR/log_xxx.qlog.zst
	*bufio.Writer          // buffering the f
}

func newQlogger(qlogDir string, role logging.Perspective, connID []byte) io.WriteCloser {
	t := time.Now().UTC().Format("2006-01-02T15-04-05.999999999UTC")
	r := "server"
	if role == logging.PerspectiveClient {
		r = "client"
	}
	finalFilename := fmt.Sprintf("%s%clog_%s_%s_%x.qlog.zst", qlogDir, os.PathSeparator, t, r, connID)
	filename := fmt.Sprintf("%s%c.log_%s_%s_%x.qlog.swp", qlogDir, os.PathSeparator, t, r, connID)
	f, err := os.Create(filename)
	if err != nil {
		log.Errorf("unable to create qlog file %s: %s", filename, err)
		return nil
	}
	return &qlogger{
		f:        f,
		filename: finalFilename,
		Writer:   bufio.NewWriter(f),
	}
}

func (l *qlogger) Close() error {
	defer os.Remove(l.f.Name())
	defer l.f.Close()
	if err := l.Writer.Flush(); err != nil {
		return err
	}
	if _, err := l.f.Seek(0, io.SeekStart); err != nil { // set the read position to the beginning of the file
		return err
	}
	f, err := os.Create(l.filename)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := bufio.NewWriter(f)
	c, err := zstd.NewWriter(buf, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithWindowSize(32*1024))
	if err != nil {
		return err
	}
	if _, err := io.Copy(c, l.f); err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return err
	}
	return buf.Flush()
}
