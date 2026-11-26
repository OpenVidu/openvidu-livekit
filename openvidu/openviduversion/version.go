package openviduversion

// These values must be overwritten by CI when releasing the artifact.
const (
	Service   = "openvidu-livekit-server"
	Version   = "3.10.0"
	GitCommit = "d686fd893a289c81aff33399b707407fd2c63135"
	BuildDate = "2026-11-26T17:59:44Z"
	Edition   = "ce"
)

type Info struct {
	Service   string `json:"service"`
	Version   string `json:"version"`
	GitCommit string `json:"gitCommit"`
	BuildDate string `json:"buildDate"`
	Edition   string `json:"edition"`
}

var ServerInfo = Info{
	Service:   Service,
	Version:   Version,
	GitCommit: GitCommit,
	BuildDate: BuildDate,
	Edition:   Edition,
}
