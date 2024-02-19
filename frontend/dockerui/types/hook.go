package types

// InstructionHook provides a hooking mechanism for instructions of Dockerfile.
type InstructionHook struct {
	Run *RunInstructionHook `json:"RUN,omitempty"`
}

// RunInstructionHook provides a hooking mechanism for `RUN` instruction of Dockerfile.
type RunInstructionHook struct {
	Entrypoint []string `json:"entrypoint"`
	Mounts     []Mount  `json:"mounts"`
}
