package local

import (
	"time"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type Opt struct {
	SessionManager *session.Manager
	CacheAccessor  cache.Accessor
}

func NewSource(opt Opt) (source.Source, error) {
	ls := &localSource{
		sm: opt.SessionManager,
		cm: opt.CacheAccessor,
	}
	return ls, nil
}

type localSource struct {
	sm *session.Manager
	cm cache.Accessor
}

func (ls *localSource) ID() string {
	return source.LocalScheme
}

func (ls *localSource) Resolve(ctx context.Context, id source.Identifier) (source.SourceInstance, error) {
	localIdentifier, ok := id.(*source.LocalIdentifier)
	if !ok {
		return nil, errors.Errorf("invalid local identifier %v", id)
	}

	return &localSourceHandler{
		src:         *localIdentifier,
		localSource: ls,
	}, nil
}

type localSourceHandler struct {
	src source.LocalIdentifier
	*localSource
}

func (ls *localSourceHandler) CacheKey(ctx context.Context) (string, error) {
	sessionID := ls.src.SessionID

	if sessionID == "" {
		uuid := session.FromContext(ctx)
		if uuid == "" {
			return "", errors.New("could not access local files without session")
		}
		sessionID = uuid
	}

	return "session:" + ls.src.Name + ":" + sessionID, nil
}

func (ls *localSourceHandler) Snapshot(ctx context.Context) (out cache.ImmutableRef, retErr error) {

	uuid := session.FromContext(ctx)
	if uuid == "" {
		return nil, errors.New("could not access local files without session")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := ls.sm.Get(timeoutCtx, uuid)
	if err != nil {
		return nil, err
	}

	mutable, err := ls.cm.New(ctx, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		if retErr != nil && mutable != nil {
			s, err := mutable.Freeze()
			if err == nil {
				go s.Release(context.TODO())
			}
		}
	}()

	mount, err := mutable.Mount(ctx)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mount)

	dest, err := lm.Mount()
	if err != nil {
		return nil, err
	}

	defer func() {
		if retErr != nil && lm != nil {
			lm.Unmount()
		}
	}()

	opt := filesync.FSSendRequestOpt{
		IncludePatterns:  nil,
		OverrideExcludes: false,
		DestDir:          dest,
		CacheUpdater:     nil,
	}

	if err := filesync.FSSync(ctx, caller, opt); err != nil {
		return nil, err
	}

	if err := lm.Unmount(); err != nil {
		return nil, err
	}
	lm = nil

	snap, err := mutable.ReleaseAndCommit(ctx)
	if err != nil {
		return nil, err
	}
	mutable = nil

	return snap, err
}
