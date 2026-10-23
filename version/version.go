// Package version exposes build identity without requiring a build system.
package version

import (
	"runtime"
	"runtime/debug"
)

// Version is the semantic version of this source tree. Release builds may
// override it with -ldflags "-X github.com/qeinstein/paradox/version.Version=...".
var Version = "0.1.0"

// Commit is the source revision. It is populated by release builds when
// available and falls back to Go's embedded VCS metadata.
var Commit = ""

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

func Current() Info {
	commit := Commit
	if commit == "" {
		if build, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range build.Settings {
				if setting.Key == "vcs.revision" {
					commit = setting.Value
					break
				}
			}
		}
	}
	return Info{
		Version: Version, Commit: commit, GoVersion: runtime.Version(),
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
}

func (i Info) String() string {
	if i.Commit == "" {
		return i.Version
	}
	commit := i.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return i.Version + " (" + commit + ")"
}
