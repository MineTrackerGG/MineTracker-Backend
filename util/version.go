package util

import "runtime/debug"

// GitCommit can be injected at build time with:
// -ldflags "-X MineTracker/util.GitCommit=<commit-hash>"
var GitCommit = "unknown"

func CurrentVersion() string {
	if GitCommit != "" && GitCommit != "unknown" {
		return GitCommit
	}

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	for _, setting := range buildInfo.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}

	return "unknown"
}
