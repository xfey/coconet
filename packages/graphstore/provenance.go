package graphstore

import (
	"runtime"
	"runtime/debug"
)

// Build provenance belongs to the private execution archive, not model input.
// Stripped/reproducible builds may omit VCS data; an empty revision explicitly
// means unknown rather than fabricating a repository identity at runtime.
type BuildProvenance struct {
	GoVersion     string `json:"go_version"`
	ModuleVersion string `json:"module_version,omitempty"`
	Revision      string `json:"revision,omitempty"`
	Modified      bool   `json:"modified,omitempty"`
}

func currentBuildProvenance() BuildProvenance {
	value := BuildProvenance{GoVersion: runtime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		value.ModuleVersion = info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				value.Revision = setting.Value
			case "vcs.modified":
				value.Modified = setting.Value == "true"
			}
		}
	}
	return value
}
