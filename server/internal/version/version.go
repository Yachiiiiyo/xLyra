package version

import "strings"

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = ""
)

// AgentAPI 是 Agent 接口/事件契约版本;有不兼容变化时加 1,iOS 据此做版本门槛。
const AgentAPI = 1

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	AgentAPI  int    `json:"agent_api"`
}

func Current() Info {
	info := Info{
		Version:   strings.TrimSpace(Version),
		Commit:    strings.TrimSpace(Commit),
		BuildTime: strings.TrimSpace(BuildTime),
		AgentAPI:  AgentAPI,
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	return info
}
