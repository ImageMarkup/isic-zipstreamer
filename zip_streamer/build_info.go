package zip_streamer

import (
	"runtime/debug"
)

func getVcsRevision() string {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	for _, setting := range buildInfo.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value[:8]
		}
	}

	return "dev"
}
