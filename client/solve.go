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

	"github.com/Sirupsen/logrus"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func (c *Client) Solve(ctx context.Context, r io.Reader, statusChan chan *SolveStatus, exporter string, exporterAttrs map[string]string, localDir string) error {
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

	if err := validateLocals(def, localDir); err != nil {
		return err
	}

	ref := generateID()
	eg, ctx := errgroup.WithContext(ctx)

	statusContext, cancelStatus := context.WithCancel(context.Background())
	defer cancelStatus()

	sharedKey, err := getSharedKey(localDir)
	if err != nil {
		return errors.Wrap(err, "failed to get build shared key")
	}
	s, err := session.NewSession(filepath.Base(localDir), sharedKey)
	if err != nil {
		return errors.Wrap(err, "failed to create session")
	}

	if localDir != "" {
		_, dir, _ := parseLocalDir(localDir)
		workdirProvider := filesync.NewFSSyncProvider(dir, nil)
		s.Allow(workdirProvider)
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
			Exporter:      exporter,
			ExporterAttrs: exporterAttrs,
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

func validateLocals(defs [][]byte, localDir string) error {
	k, _, err := parseLocalDir(localDir)
	if err != nil {
		return err
	}
	for _, dt := range defs {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return errors.Wrap(err, "failed to parse llb proto op")
		}
		if src := op.GetSource(); src != nil {
			if strings.HasPrefix(src.Identifier, "local://") { // TODO: just make a type property
				name := strings.TrimPrefix(src.Identifier, "local://")
				if name != k {
					return errors.Errorf("local directory %s not enabled", name)
				}
			}
		}
	}
	return nil
}

func parseLocalDir(str string) (string, string, error) {
	if str == "" {
		return "", "", nil
	}
	parts := strings.SplitN(str, "=", 2)
	if len(parts) != 2 {
		return "", "", errors.Errorf("invalid local indentifier %q, need name=dir", str)
	}
	fi, err := os.Stat(parts[1])
	if err != nil {
		return "", "", errors.Wrapf(err, "could not find %s", parts[1])
	}
	if !fi.IsDir() {
		return "", "", errors.Errorf("%s not a directory", parts[1])
	}
	return parts[0], parts[1], nil

}
