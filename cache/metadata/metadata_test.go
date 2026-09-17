package metadata

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestGetSetSearch(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()

	dbPath := filepath.Join(tmpdir, "storage.db")

	s, err := NewStore(dbPath)
	require.NoError(t, err)
	defer s.Close()

	si, ok := s.Get("foo")
	require.False(t, ok)

	v := si.Get("bar")
	require.Nil(t, v)

	v, err = NewValue("foobar")
	require.NoError(t, err)

	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, "bar", v)
	})

	err = si.Commit()
	require.NoError(t, err)

	v = si.Get("bar")
	require.NotNil(t, v)

	var str string
	err = v.Unmarshal(&str)
	require.NoError(t, err)
	require.Equal(t, "foobar", str)

	err = s.Close()
	require.NoError(t, err)

	s, err = NewStore(dbPath)
	require.NoError(t, err)
	defer s.Close()

	si, ok = s.Get("foo")
	require.True(t, ok)

	v = si.Get("bar")
	require.NotNil(t, v)

	str = ""
	err = v.Unmarshal(&str)
	require.NoError(t, err)
	require.Equal(t, "foobar", str)

	// add second item to test Search

	si, ok = s.Get("foo2")
	require.False(t, ok)

	v, err = NewValue("foobar2")
	require.NoError(t, err)

	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, "bar2", v)
	})

	err = si.Commit()
	require.NoError(t, err)

	sis, err := s.All()
	require.NoError(t, err)
	require.Equal(t, 2, len(sis))

	require.Equal(t, "foo", sis[0].ID())
	require.Equal(t, "foo2", sis[1].ID())

	v = sis[0].Get("bar")
	require.NotNil(t, v)

	str = ""
	err = v.Unmarshal(&str)
	require.NoError(t, err)
	require.Equal(t, "foobar", str)

	// clear foo, check that only foo2 exists
	err = s.Clear(sis[0].ID())
	require.NoError(t, err)

	sis, err = s.All()
	require.NoError(t, err)
	require.Equal(t, 1, len(sis))

	require.Equal(t, "foo2", sis[0].ID())

	_, ok = s.Get("foo")
	require.False(t, ok)
}

func TestIndexes(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()

	dbPath := filepath.Join(tmpdir, "storage.db")

	s, err := NewStore(dbPath)
	require.NoError(t, err)
	defer s.Close()

	var tcases = []struct {
		key, valueKey, value, index string
	}{
		{"foo1", "bar", "val1", "tag:baz"},
		{"foo2", "bar", "val2", "tag:bax"},
		{"foo3", "bar", "val3", "tag:baz"},
	}

	for _, tcase := range tcases {
		si, ok := s.Get(tcase.key)
		require.False(t, ok)

		v, err := NewValue(tcase.valueKey)
		require.NoError(t, err)
		v.Index = tcase.index

		si.Queue(func(b *bolt.Bucket) error {
			return si.SetValue(b, tcase.value, v)
		})

		err = si.Commit()
		require.NoError(t, err)
	}

	ctx := t.Context()
	sis, err := s.Search(ctx, "tag:baz", false)
	require.NoError(t, err)
	require.Equal(t, 2, len(sis))

	require.Equal(t, "foo1", sis[0].ID())
	require.Equal(t, "foo3", sis[1].ID())

	sis, err = s.Search(ctx, "tag:bax", false)
	require.NoError(t, err)
	require.Equal(t, 1, len(sis))

	require.Equal(t, "foo2", sis[0].ID())

	err = s.Clear("foo1")
	require.NoError(t, err)

	sis, err = s.Search(ctx, "tag:baz", false)
	require.NoError(t, err)
	require.Equal(t, 1, len(sis))

	require.Equal(t, "foo3", sis[0].ID())
}

func TestExternalData(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()

	dbPath := filepath.Join(tmpdir, "storage.db")

	s, err := NewStore(dbPath)
	require.NoError(t, err)
	defer s.Close()

	si, ok := s.Get("foo")
	require.False(t, ok)

	err = si.SetExternal("ext1", []byte("data"))
	require.NoError(t, err)

	dt, err := si.GetExternal("ext1")
	require.NoError(t, err)
	require.Equal(t, "data", string(dt))

	si, ok = s.Get("bar")
	require.False(t, ok)

	_, err = si.GetExternal("ext1")
	require.Error(t, err)

	si, _ = s.Get("foo")
	dt, err = si.GetExternal("ext1")
	require.NoError(t, err)
	require.Equal(t, "data", string(dt))

	err = s.Clear("foo")
	require.NoError(t, err)

	si, _ = s.Get("foo")
	_, err = si.GetExternal("ext1")
	require.Error(t, err)
}

func TestIndexReplacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		newIndex string
		remove   bool
	}{
		{name: "change index", newIndex: "tag:new"},
		{name: "remove index"},
		{name: "keep same index", newIndex: "tag:old"},
		{name: "delete value", remove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewStore(filepath.Join(t.TempDir(), "storage.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			si, _ := s.Get("record")
			old, err := NewValue("old")
			require.NoError(t, err)
			old.Index = "tag:old"
			require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, "key", old) }))
			var replacement *Value
			if !tc.remove {
				replacement, err = NewValue("new")
				require.NoError(t, err)
				replacement.Index = tc.newIndex
			}
			require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, "key", replacement) }))
			exists, err := s.Probe("tag:old")
			require.NoError(t, err)
			assert.Equal(t, tc.newIndex == "tag:old", exists, "obsolete index must disappear when the indexed value changes")
			results, err := s.Search(t.Context(), "tag:old", false)
			require.NoError(t, err)
			if tc.newIndex == "tag:old" {
				assert.Len(t, results, 1)
			} else {
				assert.Empty(t, results)
			}
			if tc.newIndex != "" {
				results, err := s.Search(t.Context(), tc.newIndex, false)
				require.NoError(t, err)
				assert.Len(t, results, 1)
			}
			require.NoError(t, s.Clear("record"))
			for _, index := range []string{"tag:old", "tag:new"} {
				exists, err := s.Probe(index)
				require.NoError(t, err)
				assert.False(t, exists, "Clear must not leave a live index key pointing at deleted metadata")
			}
		})
	}
}

func TestIndexSharedByMultipleValues(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "storage.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	si, _ := s.Get("record")
	for _, key := range []string{"first", "second"} {
		v, err := NewValue(key)
		require.NoError(t, err)
		v.Index = "shared"
		require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, key, v) }))
	}
	v, err := NewValue("replacement")
	require.NoError(t, err)
	v.Index = "replacement"
	require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, "first", v) }))
	results, err := s.Search(t.Context(), "shared", false)
	require.NoError(t, err)
	require.Len(t, results, 1, "another value still owns the shared index")
	require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, "second", nil) }))
	exists, err := s.Probe("shared")
	require.NoError(t, err)
	require.False(t, exists, "last reference to the shared index was deleted")
	results, err = s.Search(t.Context(), "replacement", false)
	require.NoError(t, err)
	require.Len(t, results, 1)
}

func TestIndexReplacementUsesStoredValue(t *testing.T) {
	for _, mode := range []string{"stale handle", "mutated value", "queued replacements"} {
		t.Run(mode, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "storage.db")
			s, err := NewStore(filename)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			si, _ := s.Get("record")
			stale, _ := s.Get("record")
			v, err := NewValue("value")
			require.NoError(t, err)
			v.Index = "old"
			require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, "key", v) }))
			if mode == "mutated value" {
				require.NoError(t, si.GetAndSetValue("key", func(v *Value) (*Value, error) { v.Index = "new"; return v, nil }))
			} else {
				v, err = NewValue("replacement")
				require.NoError(t, err)
				v.Index = "new"
				if mode == "stale handle" {
					require.NoError(t, stale.Update(func(b *bolt.Bucket) error { return stale.SetValue(b, "key", v) }))
				} else {
					for _, index := range []string{"intermediate", "new"} {
						replacement, err := NewValue(index)
						require.NoError(t, err)
						replacement.Index = index
						si.Queue(func(b *bolt.Bucket) error { return si.SetValue(b, "key", replacement) })
					}
					require.NoError(t, si.Commit())
				}
			}
			require.NoError(t, s.Close())
			s, err = NewStore(filename)
			require.NoError(t, err)
			for _, index := range []string{"old", "intermediate"} {
				exists, err := s.Probe(index)
				require.NoError(t, err)
				require.False(t, exists, "obsolete index survived reopening the database")
			}
			results, err := s.Search(t.Context(), "new", false)
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.NoError(t, s.Clear("record"))
			require.NoError(t, s.db.View(func(tx *bolt.Tx) error {
				k, _ := tx.Bucket([]byte(indexBucket)).Cursor().First()
				require.Nil(t, k, "all live index keys should have been removed")
				return nil
			}))
		})
	}
}

func TestIndexReplacementRollback(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "storage.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	si, _ := s.Get("record")
	v, err := NewValue("old value")
	require.NoError(t, err)
	v.Index = "old"
	require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, "key", v) }))
	replacement, err := NewValue("new value")
	require.NoError(t, err)
	replacement.Index = "new"
	rollback := errors.New("rollback transaction")
	err = si.Update(func(b *bolt.Bucket) error {
		if err := si.SetValue(b, "key", replacement); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	// Check durable state through a fresh handle. StorageItem's in-memory value
	// cache is not transaction-aware, so the original handle can be stale here.
	fresh, ok := s.Get("record")
	require.True(t, ok)
	var got string
	require.NoError(t, fresh.Get("key").Unmarshal(&got))
	require.Equal(t, "old value", got)
	for index, want := range map[string]bool{"old": true, "new": false} {
		exists, err := s.Probe(index)
		require.NoError(t, err)
		require.Equal(t, want, exists)
	}
	// The next update through that stale handle still clears the actual stored
	// index rather than the index from the rolled-back cached value.
	replacement, err = NewValue("final value")
	require.NoError(t, err)
	replacement.Index = "final"
	require.NoError(t, si.Update(func(b *bolt.Bucket) error { return si.SetValue(b, "key", replacement) }))
	for index, want := range map[string]bool{"old": false, "new": false, "final": true} {
		exists, err := s.Probe(index)
		require.NoError(t, err)
		require.Equal(t, want, exists)
	}
}
