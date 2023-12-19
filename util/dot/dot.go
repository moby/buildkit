// package dot converts manifest files into dot files, for visualization

package dot

import (
	"strconv"

	"github.com/azr/codenam"
	"github.com/emicklei/dot"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/opencontainers/go-digest"
)

type Config struct {
	codenamize       bool
	prefixIndex      bool
	maxNodeNameLength int
}

func Codenamize() func(*Config) {
	return func(c *Config) {
		c.codenamize = true
	}
}

func MaxNodeNameLength(i int) func(*Config) {
	return func(c *Config) {
		c.maxNodeNameLength = i
	}
}

func PrefixIndex() func(*Config) {
	return func(c *Config) {
		c.prefixIndex = true
	}
}

func FromV1CacheConfig(config v1.CacheConfig, options ...func(*Config)) (*dot.Graph, error) {
	cfg := Config{}
	for _, option := range options {
		option(&cfg)
	}
	g := dot.NewGraph(dot.Directed)
	nodes := map[string]dot.Node{}
	records := g.Subgraph("Records", dot.ClusterOption{})
	layers := g.Subgraph("Layers", dot.ClusterOption{})

	getOrAddNode := func(g *dot.Graph, dgst digest.Digest, pos int) dot.Node {
		id := dgst.String()
		if cfg.prefixIndex {
			id = strconv.Itoa(pos) + "_" + id
		}
		if _, found := nodes[id]; !found {
			var name string
			if cfg.prefixIndex {
				name = strconv.Itoa(pos) + " "
			}
			if cfg.codenamize {
				name = name + codenam.Ize(dgst.String())
				l := len("sha256:")
				name = name + " " + dgst.String()[l:l+6]
				nodes[id] = g.Node(name).Attr("tooltip", dgst.String())
			} else {
				name += dgst.String()
				if cfg.maxNodeNameLength > 0 {
					name = name[:min(len(name)-1, cfg.maxNodeNameLength)]
				}
				nodes[id] = g.Node(name)
			}
		}
		return nodes[id]
	}
	for i, record := range config.Records {
		node := getOrAddNode(records, record.Digest, i)
		if len(record.Inputs) == 0 {
			node.Attr("color", "red")
		}
		for _, inputs := range record.Inputs {
			for _, input := range inputs {
				inputRecord := config.Records[input.LinkIndex]
				inputNode := getOrAddNode(records, inputRecord.Digest, input.LinkIndex)
				g.Edge(inputNode, node)
			}
		}
		for _, result := range record.Results {
			layer := config.Layers[result.LayerIndex]
			layerNode := getOrAddNode(layers, layer.Blob, result.LayerIndex)
			g.Edge(node, layerNode).Attr("color", "blue")
		}
	}
	for i, layer := range config.Layers {
		layerNode := getOrAddNode(layers, layer.Blob, i)
		if layer.ParentIndex == -1 {
			layerNode.Attr("color", "red")
			continue
		}
		parentLayerNode := getOrAddNode(layers, config.Layers[layer.ParentIndex].Blob, layer.ParentIndex)
		g.Edge(parentLayerNode, layerNode)
	}

	return g, nil
}
