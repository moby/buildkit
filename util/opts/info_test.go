package opts

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInfo(t *testing.T) {
	type S struct {
		P string `opt:"p"`
	}
	type T struct {
		A string   `opt:"a"`
		B int      `opt:"b" name:"B" help:"this is b"`
		C string   `opt:"c,x/y"`
		D []string `opt:"d,remain"`
		E S        `opt:"e,squash"`
		F bool     `opt:"f"`
		G string   `opt:"g,required"`
		X string   `opt:"-" name:"XXX" help:"this is invisible"`
	}

	infos, err := Info(&T{F: true})
	require.NoError(t, err)
	require.Equal(t, 8, len(infos))

	require.Equal(t, "a", infos[0].Key)
	require.Equal(t, "string", infos[0].Type)

	require.Equal(t, "b", infos[1].Key)
	require.Equal(t, "int", infos[1].Type)
	require.Equal(t, "B", infos[1].Name)
	require.Equal(t, "this is b", infos[1].Help)

	require.Equal(t, "c", infos[2].Key)
	require.Equal(t, "string", infos[2].Type)
	require.Equal(t, []string{"x", "y"}, infos[2].Choices)

	require.Equal(t, "d", infos[3].Key)
	require.Equal(t, "", infos[3].Type)
	require.Equal(t, true, infos[3].Remain)

	require.Equal(t, "e", infos[4].Key)
	require.Equal(t, "", infos[4].Type)
	require.Equal(t, true, infos[4].Squash)
	require.Equal(t, 1, len(infos[4].Children))
	require.Equal(t, "p", infos[4].Children[0].Key)
	require.Equal(t, "string", infos[4].Children[0].Type)

	require.Equal(t, "f", infos[5].Key)
	require.Equal(t, "bool", infos[5].Type)
	require.Equal(t, true, infos[5].Default)

	require.Equal(t, "g", infos[6].Key)
	require.Equal(t, "string", infos[6].Type)
	require.Equal(t, true, infos[6].Required)

	require.Equal(t, "", infos[7].Key)
	require.Equal(t, "string", infos[7].Type)
	require.Equal(t, "XXX", infos[7].Name)
	require.Equal(t, "this is invisible", infos[7].Help)
}

func TestInfoNestedStructs(t *testing.T) {
	type R struct {
		P string
	}
	type S struct {
		R `opt:"r,squash"`
	}
	type T struct {
		S `opt:"s,squash"`
	}

	infos, err := Info(&T{})
	require.NoError(t, err)
	require.Equal(t, 1, len(infos))

	require.Equal(t, "s", infos[0].Key)
	require.Equal(t, 1, len(infos[0].Children))
	require.Equal(t, "r", infos[0].Children[0].Key)
	require.Equal(t, 1, len(infos[0].Children[0].Children))
	require.Equal(t, "p", infos[0].Children[0].Children[0].Key)
	require.Equal(t, "string", infos[0].Children[0].Children[0].Type)
	require.Equal(t, 0, len(infos[0].Children[0].Children[0].Children))
}

func TestInfoPrivate(t *testing.T) {
	target := struct {
		x string
	}{}

	require.NotPanics(t, func() {
		Info(&target)
	})
}
