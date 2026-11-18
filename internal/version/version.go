// Package version exposes build identity for Gauge.
//
// Version, Commit and BuildDate are intended to be set at build time with
// -ldflags "-X github.com/m4ns0ur/gauge/internal/version.Version=...".
package version

import "fmt"

var (
	// Version is the semantic version of this build.
	Version = "0.1.0-dev"
	// Commit is the Git commit this binary was built from.
	Commit = "unknown"
	// BuildDate is the UTC build timestamp in RFC 3339 form.
	BuildDate = "unknown"
)

// Info is the machine-readable description of a Gauge build.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

// Get returns the build identity.
func Get() Info {
	return Info{
		Name:      "gauge",
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
	}
}

// String renders a single human-readable version line.
func (i Info) String() string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", i.Name, i.Version, i.Commit, i.BuildDate)
}
