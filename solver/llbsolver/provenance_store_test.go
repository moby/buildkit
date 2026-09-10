package llbsolver

import (
	"testing"

	"github.com/moby/buildkit/client/llb"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/stretchr/testify/require"
)

func TestProvenanceStoreLooksUpByDefinitionDigest(t *testing.T) {
	ctx := t.Context()
	def, err := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("foo"))).Marshal(ctx)
	require.NoError(t, err)

	pbDef := def.ToPB()
	pbDefBefore := pbDef.CloneVT()
	store := newProvenanceStore()
	req := &provenancetypes.RequestProvenance{
		Request: &provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args: map[string]string{
				"target": "base",
			},
		},
	}

	recordID, err := store.register(pbDef, req)
	require.NoError(t, err)
	require.NotEmpty(t, recordID)
	require.Equal(t, pbDefBefore, pbDef)

	in, ok := store.lookup(pbDef)
	require.True(t, ok)
	require.NotNil(t, in.Request)
	require.Equal(t, "dockerfile.v0", in.Request.Frontend)
	require.Equal(t, "base", in.Request.Args["target"])

	otherDef, err := llb.Scratch().File(llb.Mkfile("bar", 0600, []byte("bar"))).Marshal(ctx)
	require.NoError(t, err)
	forged := otherDef.ToPB()

	_, ok = store.lookup(forged)
	require.False(t, ok)
}

func TestProvenanceStoreOmitsInputRoot(t *testing.T) {
	ctx := t.Context()
	def, err := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("foo"))).Marshal(ctx)
	require.NoError(t, err)

	pbDef := def.ToPB()
	store := newProvenanceStore()
	req := &provenancetypes.RequestProvenance{
		Request: &provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args:     map[string]string{"target": "base"},
			Root: &provenancetypes.RequestProvenance{
				Request: &provenancetypes.Parameters{
					Frontend: "gateway.v0",
					Args:     map[string]string{"source": "dockerfile.v0"},
				},
			},
		},
	}

	recordID, err := store.register(pbDef, req)
	require.NoError(t, err)
	require.NotEmpty(t, recordID)

	in, ok := store.lookup(pbDef)
	require.True(t, ok)
	require.NotNil(t, in.Request)
	require.Equal(t, "dockerfile.v0", in.Request.Frontend)
	require.Nil(t, in.Request.Root)
}

func TestProvenanceStoreLookupAfterDefinitionOpRoundTrip(t *testing.T) {
	ctx := t.Context()
	def, err := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("foo"))).Marshal(ctx)
	require.NoError(t, err)

	pbDef := def.ToPB()
	store := newProvenanceStore()
	req := &provenancetypes.RequestProvenance{
		Request: &provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args:     map[string]string{"target": "base"},
		},
	}

	recordID, err := store.register(pbDef, req)
	require.NoError(t, err)
	require.NotEmpty(t, recordID)

	op, err := llb.NewDefinitionOp(pbDef)
	require.NoError(t, err)
	st := llb.NewState(op)
	roundTripDef, err := st.Marshal(ctx)
	require.NoError(t, err)

	in, ok := store.lookup(roundTripDef.ToPB())
	require.True(t, ok)
	require.Equal(t, "dockerfile.v0", in.Request.Frontend)
}

func TestProvenanceStoreUnregister(t *testing.T) {
	ctx := t.Context()
	def, err := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("foo"))).Marshal(ctx)
	require.NoError(t, err)

	pbDef := def.ToPB()
	store := newProvenanceStore()
	req := &provenancetypes.RequestProvenance{
		Request: &provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
		},
	}

	recordID, err := store.register(pbDef, req)
	require.NoError(t, err)
	require.NotEmpty(t, recordID)

	_, ok := store.lookup(pbDef)
	require.True(t, ok)

	store.unregister([]string{recordID})
	_, ok = store.lookup(pbDef)
	require.False(t, ok)
}

func TestProvenanceStoreAmbiguousDigest(t *testing.T) {
	ctx := t.Context()
	def, err := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("foo"))).Marshal(ctx)
	require.NoError(t, err)

	pbDef := def.ToPB()
	store := newProvenanceStore()
	_, err = store.register(pbDef, &provenancetypes.RequestProvenance{
		Request: &provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args:     map[string]string{"target": "base"},
		},
	})
	require.NoError(t, err)
	_, err = store.register(pbDef, &provenancetypes.RequestProvenance{
		Request: &provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args:     map[string]string{"target": "other"},
		},
	})
	require.NoError(t, err)

	_, ok := store.lookup(pbDef)
	require.False(t, ok)
}

func TestScrubMinRequestScrubsNestedRequests(t *testing.T) {
	req := &provenancetypes.Parameters{
		Args: map[string]string{
			"build-arg:FOO": "bar",
			"label:lbl":     "abc",
			"context":       "input:base",
		},
		Secrets: []*provenancetypes.Secret{{ID: "secret"}},
		SSH:     []*provenancetypes.SSH{{ID: "default"}},
		Inputs: map[string]*provenancetypes.RequestProvenance{
			"base": {
				Request: &provenancetypes.Parameters{
					Args: map[string]string{
						"target":              "base",
						"build-arg:BASE_TEXT": "from-input",
						"label:input":         "nested",
					},
					Secrets: []*provenancetypes.Secret{{ID: "input-secret"}},
					SSH:     []*provenancetypes.SSH{{ID: "input-ssh"}},
				},
			},
		},
		Root: &provenancetypes.RequestProvenance{
			Request: &provenancetypes.Parameters{
				Args: map[string]string{
					"source":         "dockerfile.v0",
					"build-arg:ROOT": "root",
				},
			},
		},
	}

	require.True(t, scrubMinRequest(req))

	require.Equal(t, map[string]string{"context": "input:base"}, req.Args)
	require.Nil(t, req.Secrets)
	require.Nil(t, req.SSH)

	require.Contains(t, req.Inputs, "base")
	require.Equal(t, map[string]string{"target": "base"}, req.Inputs["base"].Request.Args)
	require.Nil(t, req.Inputs["base"].Request.Secrets)
	require.Nil(t, req.Inputs["base"].Request.SSH)

	require.NotNil(t, req.Root)
	require.Equal(t, map[string]string{"source": "dockerfile.v0"}, req.Root.Request.Args)
}
