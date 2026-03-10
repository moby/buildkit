package cpuset

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	t.Parallel()

	t.Run("empty is valid", func(t *testing.T) {
		set, err := Parse("")
		require.NoError(t, err)
		require.Empty(t, set)
	})

	t.Run("single value", func(t *testing.T) {
		set, err := Parse("3")
		require.NoError(t, err)
		require.Equal(t, map[int]struct{}{3: {}}, set)
	})

	t.Run("range expands inclusively", func(t *testing.T) {
		set, err := Parse("0-3")
		require.NoError(t, err)
		require.Equal(t, map[int]struct{}{0: {}, 1: {}, 2: {}, 3: {}}, set)
	})

	t.Run("comma list and range mix", func(t *testing.T) {
		set, err := Parse("0,2-3,5")
		require.NoError(t, err)
		require.Equal(t, map[int]struct{}{0: {}, 2: {}, 3: {}, 5: {}}, set)
	})

	t.Run("whitespace tolerated", func(t *testing.T) {
		set, err := Parse(" 0 , 2 - 3 ")
		require.NoError(t, err)
		require.Equal(t, map[int]struct{}{0: {}, 2: {}, 3: {}}, set)
	})

	t.Run("negative rejected", func(t *testing.T) {
		_, err := Parse("-1")
		require.Error(t, err)
	})

	t.Run("non-numeric rejected", func(t *testing.T) {
		_, err := Parse("abc")
		require.Error(t, err)
	})

	t.Run("reversed range rejected", func(t *testing.T) {
		_, err := Parse("5-2")
		require.Error(t, err)
	})

	t.Run("element at max accepted", func(t *testing.T) {
		set, err := Parse(strconv.Itoa(MaxCPU))
		require.NoError(t, err)
		require.Equal(t, map[int]struct{}{MaxCPU: {}}, set)
	})

	t.Run("element above max rejected", func(t *testing.T) {
		_, err := Parse(strconv.Itoa(MaxCPU + 1))
		require.Error(t, err)
	})

	t.Run("range above max rejected without huge allocation", func(t *testing.T) {
		_, err := Parse("0-999999999")
		require.Error(t, err)
	})
}

func TestFormat(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		require.Equal(t, "", Format(nil))
		require.Equal(t, "", Format(map[int]struct{}{}))
	})

	t.Run("single value", func(t *testing.T) {
		require.Equal(t, "3", Format(map[int]struct{}{3: {}}))
	})

	t.Run("collapses contiguous range", func(t *testing.T) {
		set := map[int]struct{}{0: {}, 1: {}, 2: {}, 3: {}}
		require.Equal(t, "0-3", Format(set))
	})

	t.Run("preserves gaps", func(t *testing.T) {
		set := map[int]struct{}{0: {}, 2: {}, 3: {}, 5: {}}
		require.Equal(t, "0,2-3,5", Format(set))
	})

	t.Run("round-trip", func(t *testing.T) {
		const input = "0,2-3,5-9,11"
		set, err := Parse(input)
		require.NoError(t, err)
		require.Equal(t, input, Format(set))
	})
}

func TestValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, Validate(""))
	require.NoError(t, Validate("0-3"))
	require.NoError(t, Validate("0,2-3,5"))

	require.Error(t, Validate("abc"))
	require.Error(t, Validate("5-2"))
	require.Error(t, Validate("-1"))
}
