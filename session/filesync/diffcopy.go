package filesync

import (
	"bufio"
	"context"
	io "io"
	"os"
	strings "strings"
	"time"

	"github.com/moby/buildkit/util/bklog"

	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	copy "github.com/tonistiigi/fsutil/copy"
	fstypes "github.com/tonistiigi/fsutil/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Stream interface {
	Context() context.Context
	SendMsg(m interface{}) error
	RecvMsg(m interface{}) error
}

func sendDiffCopy(stream Stream, fs fsutil.FS, progress progressCb) error {
	return errors.WithStack(fsutil.Send(stream.Context(), stream, fs, progress))
}

func newStreamWriter(stream grpc.ClientStream, id string) io.WriteCloser {
	wc := &streamWriterCloser{ClientStream: stream, id: id}
	return &bufferedWriteCloser{Writer: bufio.NewWriter(wc), Closer: wc}
}

type bufferedWriteCloser struct {
	*bufio.Writer
	io.Closer
}

func (bwc *bufferedWriteCloser) Close() error {
	if err := bwc.Writer.Flush(); err != nil {
		return errors.WithStack(err)
	}
	return bwc.Closer.Close()
}

type streamWriterCloser struct {
	grpc.ClientStream
	id string
}

func (wc *streamWriterCloser) Write(dt []byte) (int, error) {
	if err := wc.ClientStream.SendMsg(&BytesMessage{ID: wc.id, Data: dt}); err != nil {
		// SendMsg return EOF on remote errors
		if errors.Is(err, io.EOF) {
			if err := errors.WithStack(wc.ClientStream.RecvMsg(struct{}{})); err != nil {
				return 0, err
			}
		}
		return 0, errors.WithStack(err)
	}
	return len(dt), nil
}

func (wc *streamWriterCloser) Close() error {
	if err := wc.ClientStream.CloseSend(); err != nil {
		return errors.WithStack(err)
	}
	// block until receiver is done
	var bm BytesMessage
	if err := wc.ClientStream.RecvMsg(&bm); err != io.EOF {
		return errors.WithStack(err)
	}
	return nil
}

func recvDiffCopy(ds grpc.ClientStream, dest string, cu CacheUpdater, progress progressCb, differ fsutil.DiffType, filter func(string, *fstypes.Stat) bool) (err error) {
	st := time.Now()
	defer func() {
		bklog.G(ds.Context()).Debugf("diffcopy took: %v", time.Since(st))
	}()
	var cf fsutil.ChangeFunc
	var ch fsutil.ContentHasher
	if cu != nil {
		cu.MarkSupported(true)
		cf = cu.HandleChange
		ch = cu.ContentHasher()
	}
	defer func() {
		// tracing wrapper requires close trigger even on clean eof
		if err == nil {
			ds.CloseSend()
		}
	}()
	return errors.WithStack(fsutil.Receive(ds.Context(), ds, dest, fsutil.ReceiveOpt{
		NotifyHashed:  cf,
		ContentHasher: ch,
		ProgressCb:    progress,
		Filter:        fsutil.FilterFunc(filter),
		Differ:        differ,
	}))
}

func syncTargetDiffCopy(ds grpc.ServerStream, dests []string) error {
	if len(dests) == 0 {
		return errors.New("empty list of directories to sync")
	}
	for _, dest := range dests {
		if err := os.MkdirAll(dest, 0700); err != nil {
			return errors.Wrapf(err, "failed to create synctarget dest dir %s", dest)
		}
	}
	err := fsutil.Receive(ds.Context(), ds, dests[0], fsutil.ReceiveOpt{
		Merge: true,
		Filter: func() func(string, *fstypes.Stat) bool {
			uid := os.Getuid()
			gid := os.Getgid()
			return func(p string, st *fstypes.Stat) bool {
				st.Uid = uint32(uid)
				st.Gid = uint32(gid)
				return true
			}
		}(),
	})
	for _, dest := range dests[1:] {
		if err := syncDir(context.TODO(), dests[0], dest); err != nil {
			return errors.WithStack(err)
		}
	}
	return errors.WithStack(err)
}

func syncDir(ctx context.Context, origDir, newDir string) error {
	handler := func(dst, src, xattrKey string, err error) error {
		bklog.G(ctx).Warn(err)
		return nil
	}
	opts := []copy.Opt{
		copy.WithXAttrErrorHandler(handler),
		copy.AllowWildcards,
	}
	return copy.Copy(ctx, origDir, "*", newDir, "/", opts...)
}

func writeTargetFile(ds grpc.ServerStream, fs map[string]FileOutputFunc, opts metadata.MD) (err error) {
	var wc io.WriteCloser
	for {
		bm := BytesMessage{}
		if err := ds.RecvMsg(&bm); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return errors.WithStack(err)
		}
		if wc == nil {
			md := map[string]string{}
			for k, v := range opts {
				if strings.HasPrefix(k, keyExporterMetaPrefix) {
					md[strings.TrimPrefix(k, keyExporterMetaPrefix)] = strings.Join(v, ",")
				}
			}

			if exp, ok := fs[bm.ID]; ok {
				wc, err = exp(md)
			} else {
				// Legacy case - default to the first export
				for _, exp := range fs {
					wc, err = exp(md)
					break
				}
			}
			if err != nil {
				return errors.WithStack(err)
			}
			if wc == nil {
				return status.Errorf(codes.AlreadyExists, "target already exists")
			}
			defer func() {
				err1 := wc.Close()
				if err != nil {
					err = err1
				}
			}()
		}
		if _, err := wc.Write(bm.Data); err != nil {
			return errors.WithStack(err)
		}
	}
}
