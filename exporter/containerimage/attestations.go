package containerimage

import (
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

var intotoPlatform ocispecs.Platform = ocispecs.Platform{
	Architecture: "unknown",
	OS:           "unknown",
}
