package shell

import "context"

type Key struct {
	Id   string
	Name string
}

type KeyRenameObject struct {
	Id        string
	Now       string
	Overwrite bool
	Was       string
}

type keyListOutput struct {
	Keys []*Key
}

type KeyOpt func(*RequestBuilder) error
type keyGen struct{}

var KeyGen keyGen

func (keyGen) Type(alg string) KeyOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("type", alg)
		return nil
	}
}

func (keyGen) Size(size int) KeyOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("size", size)
		return nil
	}
}

// KeyGen Create a new keypair
func (s *Shell) KeyGen(ctx context.Context, name string, options ...KeyOpt) (*Key, error) {
	rb := s.Request("key/gen", name)
	for _, opt := range options {
		if err := opt(rb); err != nil {
			return nil, err
		}
	}

	var out Key
	if err := rb.Exec(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KeyList List all local keypairs
func (s *Shell) KeyList(ctx context.Context) ([]*Key, error) {
	var out keyListOutput
	if err := s.Request("key/list").Exec(ctx, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// KeyRename Rename a keypair
func (s *Shell) KeyRename(ctx context.Context, old string, new string, force bool) (*KeyRenameObject, error) {
	var out KeyRenameObject
	if err := s.Request("key/rename", old, new).
		Option("force", force).
		Exec(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KeyRm remove a keypair
func (s *Shell) KeyRm(ctx context.Context, name string) ([]*Key, error) {
	var out keyListOutput
	if err := s.Request("key/rm", name).
		Exec(ctx, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}
