package openviduversion

// These values must be overwritten by CI when releasing the artifact.
const (
	Service   = "openvidu-livekit-server"
	Version   = "3.9.0"
	GitCommit = "388caa2e035e23e7836e4dee973baf8e710da6c6"
	BuildDate = "2026-09-29T15:03:21Z"
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
