### llb-gobuild

Generate BuildKit LLB definition for building a golang package.

```
src := llb.Local("src")

gb := gobuild.New(nil)
// gb := gobuild.New(&gobuild.Opt{DevMode: true})

buildctl, err := gb.BuildExe(gobuild.BuildOpt{
	Source:    src,
	MountPath: "/go/src/github.com/moby/buildkit",
	Pkg:       "github.com/moby/buildkit/cmd/buildctl",
	BuildTags: []string{},
})
if err != nil {
	return err
}
```
