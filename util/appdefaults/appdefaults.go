package appdefaults

type contextKey string

const (
	BridgeName               = "buildkit0"
	BridgeSubnet             = "10.10.0.0/16"
	ContextKeyCustomFrontend = contextKey("custom.frontend.context.flag")
)
