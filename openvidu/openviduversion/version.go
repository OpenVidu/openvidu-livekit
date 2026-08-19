package openviduversion

// These values must be overwritten by CI when releasing the artifact.
const (
	Service   = "openvidu-livekit-server"
	Version   = "3.8.0"
	GitCommit = "unknown"
	BuildDate = "unknown"
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
