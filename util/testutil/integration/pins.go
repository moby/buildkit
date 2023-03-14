package integration

var pins = map[string]map[string]string{
	// busybox is pinned to 1.35. Newer produces has "illegal instruction" panic on some of Github infra on sha256sum
	"busybox:latest": {
		"amd64":   "sha256:0d5a701f0ca53f38723108687add000e1922f812d4187dea7feaee85d2f5a6c5",
		"arm64v8": "sha256:ffe38d75e44d8ffac4cd6d09777ffc31e94ea0ded6a0164e825a325dc17a3b68",
		"library": "sha256:f4ed5f2163110c26d42741fdc92bd1710e118aed4edb19212548e8ca4e5fca22",
	},
	"alpine:latest": {
		"amd64":   "sha256:c0d488a800e4127c334ad20d61d7bc21b4097540327217dfab52262adc02380c",
		"arm64v8": "sha256:af06af3514c44a964d3b905b498cf6493db8f1cde7c10e078213a89c87308ba0",
		"library": "sha256:8914eb54f968791faf6a8638949e480fef81e697984fba772b3976835194c6d4",
	},
}
