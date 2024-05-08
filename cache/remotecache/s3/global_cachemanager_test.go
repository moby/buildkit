package s3

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/big_manifest.json
var bigManifest string

func loadBigManifest(config *v1.CacheConfig) error {
	return json.NewDecoder(strings.NewReader(bigManifest)).Decode(config)
}

func TestMarshal(t *testing.T) {
	var v1cfg v1.CacheConfig
	require.NoError(t, loadBigManifest(&v1cfg))

	files, err := MarshallSplitManifests(&v1cfg)
	require.NoError(t, err)

	exists := func(id string) bool {
		key := manifests + id
		_, exists := files[key]
		return exists
	}

	walkLinks := func(id string, link solver.CacheInfoLink, fn func(id string) error) error {
		recordKey := outputKey(link.Digest, link.Output)
		prefix := links + vertexPrefix(id, recordKey, int(link.Input), link.Selector)
		for filename := range files {
			if strings.HasPrefix(filename, prefix) {
				id := strings.TrimPrefix(filename, prefix)
				if ids := strings.Split(id, separator); len(ids) > 1 {
					id = ids[len(ids)-1]
				}
				fn(id)
			}
		}
		return nil
	}
	s3Client := s3Client{}
	walkResults := func(id string, fn func(r solver.CacheResult) error) error {
		manifestKey := manifests + id
		manifesti, found := files[manifestKey]
		if !found {
			return nil
		}
		manifest, ok := manifesti.(MiniManifest)
		if !ok {
			return nil
		}
		allLayers := v1.DescriptorProvider{}

		for _, l := range manifest.Layers {
			dpp, err := s3Client.makeDescriptorProviderPair(l)
			require.NoError(t, err)
			allLayers[l.Blob] = *dpp
		}
		remote, err := manifest.getRemoteChain(allLayers)
		if err != nil {
			return err
		}

		return fn(solver.CacheResult{
			ID:        remoteID(remote),
			CreatedAt: manifest.CreatedAt,
		})
	}

	walkBackLinks := func(id string, fn func(id string, link solver.CacheInfoLink) error) error {
		prefix := backlinks + id
		for filename := range files {
			if strings.HasPrefix(filename, prefix) {
				filename = strings.TrimPrefix(filename, backlinks)
				parts := strings.Split(filename, separator)
				if len(parts) != 5 {
					// TODO: log something
					continue
				}
				parts2, err := strconv.Atoi(parts[2])
				if err != nil {
					// TODO: log something
					continue
				}
				// `ID::dgst::inputIdx::selector::parent.ID`
				// backlink from parent to id
				fn(parts[4], solver.CacheInfoLink{
					Digest:   digest.Digest(parts[1]),
					Input:    solver.Index(parts2),
					Selector: digest.Digest(parts[3]),
				})
			}
		}
		return nil
	}

	// nappyMan has undesirableDepression{lyricalInspection} as a son, trough
	// faithfulDrive.
	// graph : nappy-man -> lyrical-inspection     -> psychotic-tonight
	// id are: nappy-man -> undesirable-depression -> sparkling-hide
	//
	// Note: in this manifests there are many "psychotic-tonight" but only one
	// with id sparkling-hide. Getting backlinks from sparkling-hide, should
	// only return undesirable-depression and not any of the parents of the
	// other "psychotic-tonight".
	nappyMan := "sha256:742cda66da50c4d8a26d8b7ed284bb9d69388676afacf97c1b3ccbf883ca96f7"
	faithfulDrive := "sha256:d3b5fc25aaf9122773e0c50953a63376786c79699a8030ddc43c3e57a6ed115f"
	// lyricalInspection := outputKey(faithfulDrive, 0)
	undesirableDepression := "sha256:86cb8ab53fab69f1d12d010358832dded552cd9b65c4678461a6b9bdaff11193"
	sparklingHide := "sha256:c780bf67e4c7e1a114654da9ea2475d8093443eec25d2313587ba172c50be06d"

	shrillCourage := "sha256:5b14ae8eb06563886c52c23a82883aa16680291c9bb3b0b4c402f8a72f9a2200"
	// psychoticTonight := outputKey(shrillCourage, 0)

	require.True(t, exists(nappyMan))
	require.True(t, exists(undesirableDepression))
	require.True(t, exists(sparklingHide))

	foundIDs := []string{}
	walkLinks(nappyMan, solver.CacheInfoLink{
		Input:  0,
		Output: 0,
		Digest: digest.Digest(faithfulDrive),
		// Selector: digest.Digest(""),
	}, func(id string) error {
		foundIDs = append(foundIDs, id)
		return nil
	})
	if diff := cmp.Diff([]string{undesirableDepression}, foundIDs); diff != "" {
		t.Fatalf("walkLinks %s", diff)
	}

	foundResults := []solver.CacheResult{}
	walkResults(undesirableDepression, func(r solver.CacheResult) error {
		foundResults = append(foundResults, r)
		return nil
	})
	require.Len(t, foundResults, 1)

	foundIDs = []string{}
	walkLinks(undesirableDepression, solver.CacheInfoLink{
		Input:  0,
		Output: 0,
		Digest: digest.Digest(shrillCourage),
		// Selector: "",
	}, func(id string) error {
		foundIDs = append(foundIDs, id)
		return nil
	})
	if diff := cmp.Diff([]string{sparklingHide}, foundIDs); diff != "" {
		t.Fatalf("walkLinks %s", diff)
	}

	foundIDs = []string{}
	links := []solver.CacheInfoLink{}
	walkBackLinks(sparklingHide, func(id string, link solver.CacheInfoLink) error {
		foundIDs = append(foundIDs, id)
		links = append(links, link)
		return nil
	})
	sort.Strings(foundIDs) // things are stored in an unordered map

	tabooOffer := "sha256:529198c0e9c1d86a8a07fb61a4dc8a406f071dc363bbc3b369ea82fab4d808db"
	if diff := cmp.Diff([]string{
		tabooOffer, // a parent and a root node
		undesirableDepression,
	}, foundIDs); diff != "" {
		t.Fatalf("walkBackLinks: sparklingHide should have two parents %s", diff)
	}
}
