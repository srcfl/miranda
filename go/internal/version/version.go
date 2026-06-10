// Package version holds the build-time version stamped via -ldflags. GoReleaser
// overrides these at release; a plain `go build` leaves the dev defaults.
package version

import "fmt"

var (
	Version = "dev"     // set via -ldflags -X .../version.Version=...
	Commit  = "none"    // short git sha
	Date    = "unknown" // RFC3339 build date
)

// String renders a one-line human version, e.g. "1.2.3 (abc1234, 2026-06-08)".
func String() string {
	date := Date
	if len(date) >= 10 {
		date = date[:10] // trim RFC3339 to YYYY-MM-DD
	}
	return fmt.Sprintf("%s (%s, %s)", Version, Commit, date)
}
