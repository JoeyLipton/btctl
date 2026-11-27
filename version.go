package main

import (
	"runtime/debug"
	"strings"
)

// version is set by release builds: go build -ldflags "-X main.version=v1.2.3".
var version = ""

// versionString describes this build. Release builds report the version they
// were stamped with; `go install module@version` builds report the module
// version; local builds report the git revision Go embedded at build time.
func versionString() string {
	v := version
	info, ok := debug.ReadBuildInfo()
	if v == "" && ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	if v == "" {
		v = "dev"
	}
	if !ok {
		return "btctl " + v
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	details := []string{info.GoVersion}
	if revision != "" && !strings.Contains(v, revision[:7]) {
		if modified == "true" {
			revision += "-dirty"
		}
		details = append([]string{revision}, details...)
	}
	return "btctl " + v + " (" + strings.Join(details, ", ") + ")"
}
