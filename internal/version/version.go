package version

import "fmt"

var (
	Version   = "v1.0.0"
	Commit    = "unknown"
	BuildDate = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
	}
}

func String() string {
	info := Get()
	return fmt.Sprintf("Version:   %s\nCommit:    %s\nBuildDate: %s", info.Version, info.Commit, info.BuildDate)
}
