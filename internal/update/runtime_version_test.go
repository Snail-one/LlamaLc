package update

import "testing"

func TestLlamaVersionOutputMatchesReleaseTag(t *testing.T) {
	for _, test := range []struct {
		output, tag string
		want        bool
	}{
		{"version: 10333 (abcdef)", "b10333", true},
		{"Version: 010333 (abcdef)", "b10333", true},
		{"version: 10334 (abcdef)", "b10333", false},
		{"version: b10333 (abcdef)", "b10333", true},
		{"llama.cpp b10333", "b10333", true},
		{"llama.cpp b103330", "b10333", false},
		{"version: 0.7.0-dev (build 11849, commit 0ae0e1976)", "b11849", true},
		{"version: 0.7.0 (build 11849, commit 0ae0e1976)", "b11849", true},
		{"Version: v0.7.0-rc.1+test (build 011849, commit ABCDEF)", "b11849", true},
		{"version: 0.7.0-dev (build 11849)", "b11849", true},
		{"version: 0.7.0-dev (build 11850, commit 0ae0e1976)", "b11849", false},
		{"version: 0.7.0-dev (build 118490, commit 0ae0e1976)", "b11849", false},
		{"version: 0.7.0-dev (build 11850, commit 0ae0e1976) b11849", "b11849", false},
		{"version: 0.7.0-dev (commit b11849)", "b11849", false},
		{"version: 0.7.0-dev", "b0", false},
		{"version: 0.7.0-dev (build 11849, commit 0ae0e1976)", "b0", false},
		{"version: unknown (build 11849, commit 0ae0e1976)", "b11849", false},
		{"version: 0.7.0-dev (build 11849xyz)", "b11849", false},
		{"version: 0.7.0-dev (build -11849)", "b11849", false},
		{"version: 0.7.0-dev (build 11849, commit 0ae0e1976)", "v0.7.0", false},
	} {
		if got := matchesLlamaTag(test.output, test.tag); got != test.want {
			t.Errorf("matchesLlamaTag(%q, %q)=%v, want %v", test.output, test.tag, got, test.want)
		}
	}
}
