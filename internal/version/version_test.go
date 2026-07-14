package version

import "testing"

func TestGetAndString(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := Version, Commit, BuildDate
	Version, Commit, BuildDate = "v1.2.3", "abc1234", "2026-07-14T12:00:00Z"
	t.Cleanup(func() {
		Version, Commit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	info := Get()
	if info.Version != Version || info.Commit != Commit || info.BuildDate != BuildDate {
		t.Fatalf("unexpected info: %#v", info)
	}
	want := "Version:   v1.2.3\nCommit:    abc1234\nBuildDate: 2026-07-14T12:00:00Z"
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
