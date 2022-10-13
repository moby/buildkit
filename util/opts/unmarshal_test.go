package opts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNothing(t *testing.T) {
	target := struct{}{}

	require.NoError(t, UnmarshalCSV("", &target))
}

func TestLoadString(t *testing.T) {
	target := struct {
		X string
	}{}

	require.NoError(t, UnmarshalCSV("x=testing", &target))
	require.Equal(t, "testing", target.X)
}

func TestLoadInts(t *testing.T) {
	target := struct {
		I int
		J uint
		K int32
		L uint8
	}{}

	require.NoError(t, UnmarshalCSV("i=1,j=2,k=3,l=4", &target))
	require.Equal(t, int(1), target.I)
	require.Equal(t, uint(2), target.J)
	require.Equal(t, int32(3), target.K)
	require.Equal(t, uint8(4), target.L)
}

func TestLoadFloats(t *testing.T) {
	target := struct {
		F float32
		G float64
	}{}

	require.NoError(t, UnmarshalCSV("f=3.14,g=2.71", &target))
	require.Equal(t, float32(3.14), target.F)
	require.Equal(t, float64(2.71), target.G)
}

func TestLoadBools(t *testing.T) {
	target := struct {
		B bool
		C bool
		D bool
	}{}

	require.NoError(t, UnmarshalCSV("b=true,c=false,d", &target))
	require.Equal(t, true, target.B)
	require.Equal(t, false, target.C)
	require.Equal(t, true, target.D)
}

func TestLoadPointers(t *testing.T) {
	target := struct {
		B   *bool
		I   *int
		I64 *int64
		S   *string
	}{}

	require.NoError(t, UnmarshalCSV("b=true,i=123,i64=456,s=foo", &target))
	require.Equal(t, true, *target.B)
	require.Equal(t, 123, *target.I)
	require.Equal(t, int64(456), *target.I64)
	require.Equal(t, "foo", *target.S)
}

func TestUnknown(t *testing.T) {
	target := struct{}{}
	require.Error(t, UnmarshalCSV("x=test", &target))
}

func TestCustomNames(t *testing.T) {
	target := struct {
		Foo string `opt:"bar"`
	}{}

	require.Error(t, UnmarshalCSV("foo=test", &target))
	require.Equal(t, "", target.Foo)
	require.NoError(t, UnmarshalCSV("bar=test", &target))
	require.Equal(t, "test", target.Foo)
}

func TestGlobbedName(t *testing.T) {
	target := struct {
		Foo string `opt:"bar.*"`
	}{}

	require.NoError(t, UnmarshalCSV("bar.x=hello", &target))
	require.Equal(t, "hello", target.Foo)
	require.NoError(t, UnmarshalCSV("bar.y=world", &target))
	require.Equal(t, "world", target.Foo)
}

func TestEmptyName(t *testing.T) {
	target := struct {
		Foo string `opt:"-"`
	}{}

	require.Error(t, UnmarshalCSV("foo=test", &target))
	require.Equal(t, "", target.Foo)

	require.Error(t, UnmarshalCSV("-=test", &target))
	require.Equal(t, "", target.Foo)
}

func TestDefaults(t *testing.T) {
	target := struct {
		Foo string
		Bar string
	}{
		Foo: "foo",
		Bar: "bar",
	}

	require.NoError(t, UnmarshalCSV("foo=123", &target))
	require.Equal(t, "123", target.Foo)
	require.Equal(t, "bar", target.Bar)
}

func TestStructs(t *testing.T) {
	target := struct {
		W string
		X struct {
			Y string
			Z string
		}
	}{}
	require.Error(t, UnmarshalCSV("w=a,y=b,z=c", &target))

	target2 := struct {
		W string
		X struct {
			Y string
			Z string
		} `opt:",squash"`
	}{}
	require.NoError(t, UnmarshalCSV("w=a,y=b,z=c", &target2))
	require.Equal(t, "a", target2.W)
	require.Equal(t, "b", target2.X.Y)
	require.Equal(t, "c", target2.X.Z)
}

func TestRemain(t *testing.T) {
	src := "x=a,y=b,z=c"

	target0 := struct {
		Y    string
		Rest int `opt:",remain"`
	}{}
	require.Error(t, UnmarshalCSV(src, &target0))

	target1 := struct {
		Y    string
		Rest map[string]string `opt:",remain"`
	}{}
	require.NoError(t, UnmarshalCSV(src, &target1))
	require.Equal(t, "b", target1.Y)
	require.Equal(t, map[string]string{"x": "a", "z": "c"}, target1.Rest)

	target2 := struct {
		Y    string
		Rest []string `opt:",remain"`
	}{}
	require.NoError(t, UnmarshalCSV(src, &target2))
	require.Equal(t, "b", target2.Y)
	require.Equal(t, []string{"x=a", "z=c"}, target2.Rest)

	target3 := struct {
		Y    string
		Rest string `opt:",remain"`
	}{}
	require.NoError(t, UnmarshalCSV(src, &target3))
	require.Equal(t, "b", target3.Y)
	require.Equal(t, "x=a,z=c", target3.Rest)
}

func TestRequired(t *testing.T) {
	target := struct {
		X string `opt:"x,required"`
		Y string `opt:"y"`
	}{}

	require.Error(t, UnmarshalCSV("y=b", &target))
	require.Equal(t, "", target.X)
	require.NoError(t, UnmarshalCSV("x=a", &target))
	require.Equal(t, "a", target.X)
}

func TestChoices(t *testing.T) {
	target := struct {
		X string `opt:"x,y/z"`
	}{}

	require.Error(t, UnmarshalCSV("x=a", &target))
	require.Equal(t, "", target.X)
	require.Error(t, UnmarshalCSV("x=b", &target))
	require.Equal(t, "", target.X)

	require.NoError(t, UnmarshalCSV("x=y", &target))
	require.Equal(t, "y", target.X)
	require.NoError(t, UnmarshalCSV("x=z", &target))
	require.Equal(t, "z", target.X)
}

type testCustomUnmarshalType struct {
	X string `opt:"x"`
}

func (t *testCustomUnmarshalType) UnmarshalX(key, value string) error {
	t.X = strings.ToUpper(value)
	return nil
}

func TestCustomUnmarshal(t *testing.T) {
	target := testCustomUnmarshalType{}
	require.NoError(t, UnmarshalCSV("x=test", &target))
	require.Equal(t, "TEST", target.X)
}

type testCustomUnmarshalGlobType struct {
	X map[string]string `opt:"x.*"`
}

func (t *testCustomUnmarshalGlobType) UnmarshalX(key, value string) error {
	if t.X == nil {
		t.X = map[string]string{}
	}
	key = strings.SplitN(key, ".", 2)[1]
	t.X[strings.ToUpper(key)] = strings.Repeat("x", len(value))
	return nil
}

func TestCustomUnmarshalGlob(t *testing.T) {
	target := testCustomUnmarshalGlobType{}
	require.NoError(t, UnmarshalCSV("x.a=1,x.b=22,x.c=333", &target))
	require.Equal(t, map[string]string{
		"A": "x",
		"B": "xx",
		"C": "xxx",
	}, target.X)
}

func TestUnmarshalPrivate(t *testing.T) {
	target := struct {
		x string
	}{}

	require.NotPanics(t, func() {
		UnmarshalCSV("x=123", &target)
	})
}
