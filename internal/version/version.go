// Package version is foonmail's version, set at build time from the git
// tag: go build -ldflags "-X foonmail/internal/version.Version=v1.2.3".
package version

import "runtime/debug"

// Version is set by the Makefile and the release build. Left empty, String
// falls back to the commit Go recorded in the build.
var Version string

// String returns the version, such as "v0.1.0", or "dev (abc1234)" for a
// build without one.
func String() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if rev != "" {
			if dirty {
				rev += "-dirty"
			}
			return "dev (" + rev + ")"
		}
	}
	return "dev"
}
