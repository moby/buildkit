package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

type SolveOpt struct {
	Exporter      string
	ExporterAttrs map[string]string
	LocalDirs     map[string]string
	SharedKey     string
	// Session string
}

func (c *Client) Solve(ctx context.Context, r io.Reader, opt SolveOpt, statusChan chan *SolveStatus) error {
	defer func() {
		if statusChan != nil {
			close(statusChan)
		}
	}()

	def, err := llb.ReadFrom(r)
	if err != nil {
		return errors.Wrap(err, "failed to parse input")
	}

	if len(def) == 0 {
		return errors.New("invalid empty definition")
	}

	syncedDirs, err := prepareSyncedDirs(def, opt.LocalDirs)
	if err != nil {
		return err
	}

	ref := generateID()
	eg, ctx := errgroup.WithContext(ctx)

	statusContext, cancelStatus := context.WithCancel(context.Background())
	defer cancelStatus()

	s, err := session.NewSession(defaultSessionName(), opt.SharedKey)
	if err != nil {
		return errors.Wrap(err, "failed to create session")
	}

	if len(syncedDirs) > 0 {
		s.Allow(filesync.NewFSSyncProvider(syncedDirs))
	}

	if opt.Exporter == ExporterLocal {
		outputDir, ok := opt.ExporterAttrs[exporterLocalOutputDir]
		if !ok {
			return errors.Errorf("output directory is required for local exporter")
		}
		s.Allow(filesync.NewFSSyncTarget(outputDir))
	}

	eg.Go(func() error {
		return s.Run(ctx, grpchijack.Dialer(c.controlClient()))
	})

	eg.Go(func() error {
		defer func() { // make sure the Status ends cleanly on build errors
			go func() {
				<-time.After(3 * time.Second)
				cancelStatus()
			}()
			logrus.Debugf("stopping session")
			s.Close()
		}()
		_, err = c.controlClient().Solve(ctx, &controlapi.SolveRequest{
			Ref:           ref,
			Definition:    def,
			Exporter:      opt.Exporter,
			ExporterAttrs: opt.ExporterAttrs,
			Session:       s.ID(),
		})
		if err != nil {
			return errors.Wrap(err, "failed to solve")
		}
		return nil
	})

	eg.Go(func() error {
		stream, err := c.controlClient().Status(statusContext, &controlapi.StatusRequest{
			Ref: ref,
		})
		if err != nil {
			return errors.Wrap(err, "failed to get status")
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return errors.Wrap(err, "failed to receive status")
			}
			s := SolveStatus{}
			for _, v := range resp.Vertexes {
				s.Vertexes = append(s.Vertexes, &Vertex{
					Digest:    v.Digest,
					Inputs:    v.Inputs,
					Name:      v.Name,
					Started:   v.Started,
					Completed: v.Completed,
					Error:     v.Error,
					Cached:    v.Cached,
					Parent:    v.Parent,
				})
			}
			for _, v := range resp.Statuses {
				s.Statuses = append(s.Statuses, &VertexStatus{
					ID:        v.ID,
					Vertex:    v.Vertex,
					Name:      v.Name,
					Total:     v.Total,
					Current:   v.Current,
					Timestamp: v.Timestamp,
					Started:   v.Started,
					Completed: v.Completed,
				})
			}
			for _, v := range resp.Logs {
				s.Logs = append(s.Logs, &VertexLog{
					Vertex:    v.Vertex,
					Stream:    int(v.Stream),
					Data:      v.Msg,
					Timestamp: v.Timestamp,
				})
			}
			if statusChan != nil {
				statusChan <- &s
			}
		}
	})

	return eg.Wait()
}

func generateID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func prepareSyncedDirs(defs [][]byte, localDirs map[string]string) ([]filesync.SyncedDir, error) {
	for _, d := range localDirs {
		fi, err := os.Stat(d)
		if err != nil {
			return nil, errors.Wrapf(err, "could not find %s", d)
		}
		if !fi.IsDir() {
			return nil, errors.Errorf("%s not a directory", d)
		}
	}
	dirs := make([]filesync.SyncedDir, 0, len(localDirs))
	for _, dt := range defs {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return nil, errors.Wrap(err, "failed to parse llb proto op")
		}
		if src := op.GetSource(); src != nil {
			if strings.HasPrefix(src.Identifier, "local://") { // TODO: just make a type property
				name := strings.TrimPrefix(src.Identifier, "local://")
				d, ok := localDirs[name]
				if !ok {
					return nil, errors.Errorf("local directory %s not enabled", name)
				}
				dirs = append(dirs, filesync.SyncedDir{Name: name, Dir: d}) // TODO: excludes
			}
		}
	}
	return dirs, nil
}

func defaultSessionName() string {
	wd, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return filepath.Base(wd)
}
