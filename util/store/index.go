package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func NewIndexedStore(root string) (content.Store, error) {
	store, err := local.NewStore(root)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path.Join(root, "index.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var idx ocispecs.Index
	if err := json.NewDecoder(f).Decode(&idx); err != nil {
		return nil, err
	}

	return &indexedStore{
		Store: store,
		index: idx,
	}, nil
}

type indexedStore struct {
	content.Store
	index ocispecs.Index
}

func (s *indexedStore) Walk(ctx context.Context, fn content.WalkFunc, fs ...string) error {
	found := false
	for _, f := range fs {
		if f == "index==true" {
			found = true
			break
		}
	}
	if found && len(fs) > 1 {
		return errors.New("index filter cannot be combined with other filters")
	}

	if !found {
		return s.Store.Walk(ctx, fn, fs...)
	}

	for _, desc := range s.index.Manifests {
		fmt.Println(desc)
		info := content.Info{
			Digest: desc.Digest,
			Size:   desc.Size,
			Labels: desc.Annotations,
		}
		if createdAt, ok := desc.Annotations[ocispecs.AnnotationCreated]; ok {
			createdAt, err := time.Parse(time.RFC3339, createdAt)
			if err != nil {
				return err
			}
			info.CreatedAt = createdAt
		}

		if err := fn(info); err != nil {
			return err
		}
	}
	return nil
}
