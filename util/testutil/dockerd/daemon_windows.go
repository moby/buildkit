package dockerd

const socketScheme = "npipe://"

func getDockerdSockPath(_, id string) string {
	return `//./pipe/dockerd-` + id
}
