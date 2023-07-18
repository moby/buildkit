package integration

var pins = map[string]map[string]string{
	// busybox is pinned to 1.35. Newer produces has "illegal instruction" panic on some of Github infra on sha256sum
	"busybox:latest": {
		"amd64":   "sha256:0d5a701f0ca53f38723108687add000e1922f812d4187dea7feaee85d2f5a6c5",
		"arm64v8": "sha256:ffe38d75e44d8ffac4cd6d09777ffc31e94ea0ded6a0164e825a325dc17a3b68",
		"library": "sha256:f4ed5f2163110c26d42741fdc92bd1710e118aed4edb19212548e8ca4e5fca22",
	},
	// alpine 3.18
	"alpine:latest": {
		"amd64":   "sha256:25fad2a32ad1f6f510e528448ae1ec69a28ef81916a004d3629874104f8a7f70",
		"arm64v8": "sha256:e3bd82196e98898cae9fe7fbfd6e2436530485974dc4fb3b7ddb69134eda2407",
		"library": "sha256:82d1e9d7ed48a7523bdebc18cf6290bdb97b82302a8a9c27d4fe885949ea94d1",
	},
}
