package local

import (
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

const keySharedKey = "local.sharedKey"

type Opt struct {
	SessionManager *session.Manager
	CacheAccessor  cache.Accessor
	MetadataStore  *metadata.Store
}

func NewSource(opt Opt) (source.Source, error) {
	ls := &localSource{
		sm: opt.SessionManager,
		cm: opt.CacheAccessor,
		md: opt.MetadataStore,
	}
	return ls, nil
}

type localSource struct {
	sm *session.Manager
	cm cache.Accessor
	md *metadata.Store
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
		id := session.FromContext(ctx)
		if id == "" {
			return "", errors.New("could not access local files without session")
		}
		sessionID = id
	}

	return "session:" + ls.src.Name + ":" + sessionID, nil
}

func (ls *localSourceHandler) Snapshot(ctx context.Context) (out cache.ImmutableRef, retErr error) {

	id := session.FromContext(ctx)
	if id == "" {
		return nil, errors.New("could not access local files without session")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := ls.sm.Get(timeoutCtx, id)
	if err != nil {
		return nil, err
	}

	sharedKey := keySharedKey + ":" + ls.src.Name + ":" + caller.SharedKey()

	var mutable cache.MutableRef
	sis, err := ls.md.Search(sharedKey)
	if err != nil {
		return nil, err
	}
	for _, si := range sis {
		if m, err := ls.cm.GetMutable(ctx, si.ID()); err == nil {
			logrus.Debugf("reusing ref for local: %s", m.ID())
			mutable = m
			break
		}
	}

	if mutable == nil {
		m, err := ls.cm.New(ctx, nil, cache.CachePolicyKeepMutable)
		if err != nil {
			return nil, err
		}
		mutable = m
		logrus.Debugf("new ref for local: %s", mutable.ID())
	}

	defer func() {
		if retErr != nil && mutable != nil {
			go mutable.Release(context.TODO())
		}
	}()

	mount, err := mutable.Mount(ctx, false)
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

	skipStoreSharedKey := false
	si, _ := ls.md.Get(mutable.ID())
	if v := si.Get(keySharedKey); v != nil {
		var str string
		if err := v.Unmarshal(&str); err != nil {
			return nil, err
		}
		skipStoreSharedKey = str == sharedKey
	}
	if !skipStoreSharedKey {
		v, err := metadata.NewValue(sharedKey)
		if err != nil {
			return nil, err
		}
		v.Index = sharedKey
		if err := si.Update(func(b *bolt.Bucket) error {
			return si.SetValue(b, sharedKey, v)
		}); err != nil {
			return nil, err
		}
		logrus.Debugf("saved %s as %s", mutable.ID(), sharedKey)
	}

	snap, err := mutable.Commit(ctx)
	if err != nil {
		return nil, err
	}
	mutable = nil

	return snap, nil
}
