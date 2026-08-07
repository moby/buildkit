package instructions

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuilderFlags(t *testing.T) {

	// ---

	bf := NewBFlags()
	bf.Args = []string{}
	require.NoErrorf(t, bf.Parse(), "Test1 of %q was supposed to work", bf.Args)

	// ---

	bf = NewBFlags()
	bf.Args = []string{"--"}
	require.NoErrorf(t, bf.Parse(), "Test2 of %q was supposed to work", bf.Args)

	// ---

	bf = NewBFlags()
	flStr1 := bf.AddString("str1", "")
	flBool1 := bf.AddBool("bool1", false)
	bf.Args = []string{}
	require.NoErrorf(t, bf.Parse(), "Test3 of %q was supposed to work: %s", bf.Args)

	require.False(t, flStr1.IsUsed(), "Test3 - str1 was not used!")
	require.False(t, flBool1.IsUsed(), "Test3 - bool1 was not used!")

	// ---

	bf = NewBFlags()
	flStr1 = bf.AddString("str1", "HI")
	flBool1 = bf.AddBool("bool1", false)
	bf.Args = []string{}

	require.NoErrorf(t, bf.Parse(), "Test4 of %q was supposed to work", bf.Args)

	require.Equal(t, "HI", flStr1.Value, "Str1 was supposed to default to: HI")
	require.False(t, flBool1.IsTrue(), "Bool1 was supposed to default to: false")
	require.False(t, flStr1.IsUsed(), "Str1 was not used!")
	require.False(t, flBool1.IsUsed(), "Bool1 was not used!")

	// ---

	bf = NewBFlags()
	bf.AddString("str1", "HI")
	bf.Args = []string{"--str1"}

	require.Errorf(t, bf.Parse(), "Test %q was supposed to fail", bf.Args)

	// ---

	bf = NewBFlags()
	flStr1 = bf.AddString("str1", "HI")
	bf.Args = []string{"--str1="}

	require.NoErrorf(t, bf.Parse(), "Test %q was supposed to work", bf.Args)
	require.Emptyf(t, flStr1.Value, "Str1 (%q) should be: %q", flStr1.Value, "")

	// ---

	bf = NewBFlags()
	flStr1 = bf.AddString("str1", "HI")
	bf.Args = []string{"--str1=BYE"}

	require.NoErrorf(t, bf.Parse(), "Test %q was supposed to work", bf.Args)
	require.Equalf(t, "BYE", flStr1.Value, "Str1 (%q) should be: %q", flStr1.Value, "BYE")

	// ---

	bf = NewBFlags()
	flBool1 = bf.AddBool("bool1", false)
	bf.Args = []string{"--bool1"}

	require.NoErrorf(t, bf.Parse(), "Test %q was supposed to work", bf.Args)
	require.True(t, flBool1.IsTrue(), "Test-b1 Bool1 was supposed to be true")

	// ---

	bf = NewBFlags()
	flBool1 = bf.AddBool("bool1", false)
	bf.Args = []string{"--bool1=true"}

	require.NoErrorf(t, bf.Parse(), "Test %q was supposed to work", bf.Args)
	require.True(t, flBool1.IsTrue(), "Test-b2 Bool1 was supposed to be true")

	// ---

	bf = NewBFlags()
	flBool1 = bf.AddBool("bool1", false)
	bf.Args = []string{"--bool1=false"}

	require.NoErrorf(t, bf.Parse(), "Test %q was supposed to work", bf.Args)
	require.False(t, flBool1.IsTrue(), "Test-b3 Bool1 was supposed to be false")

	// ---

	bf = NewBFlags()
	bf.AddBool("bool1", false)
	bf.Args = []string{"--bool1=false1"}

	require.Errorf(t, bf.Parse(), "Test %q was supposed to fail", bf.Args)

	// ---

	bf = NewBFlags()
	bf.AddBool("bool1", false)
	bf.Args = []string{"--bool2"}

	require.Errorf(t, bf.Parse(), "Test %q was supposed to fail", bf.Args)

	// ---

	bf = NewBFlags()
	flStr1 = bf.AddString("str1", "HI")
	flBool1 = bf.AddBool("bool1", false)
	bf.Args = []string{"--bool1", "--str1=BYE"}

	require.NoErrorf(t, bf.Parse(), "Test %q was supposed to work", bf.Args)
	require.Equalf(t, "BYE", flStr1.Value, "Test %s, str1 should be BYE", bf.Args)
	require.Truef(t, flBool1.IsTrue(), "Test %s, bool1 should be true", bf.Args)

	// ---

	bf = NewBFlags()
	_ = bf.AddBool("bool1", false)
	_ = bf.AddBool("bool2", false)
	_ = bf.AddBool("bool3", false)
	_ = bf.AddBool("bool4", true)
	_ = bf.AddBool("bool5", true)
	_ = bf.AddString("str1", "")
	_ = bf.AddString("str2", "")
	_ = bf.AddString("str3", "def3")
	_ = bf.AddString("str4", "def4")

	bf.Args = []string{`--bool2=false`, `--bool3`, `--bool4=true`, `--bool5`, `--str2= `, `--str3=def3`, `--str4=my-val`}

	require.NoErrorf(t, bf.Parse(), "Test %q was supposed to work", bf.Args)
	used := bf.Used()
	slices.Sort(used)
	actual := strings.Join(used, ", ")
	require.Equalf(t, "bool2, bool3, bool4, bool5, str2, str3, str4", actual, "Test %s, expected '%s', got '%s'", bf.Args, "bool2, bool3, bool4, bool5, str2, str3, str4", actual)
}
