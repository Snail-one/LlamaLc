package update

import (
	"github.com/Snail-one/LlamaLc/internal/layout"
	"path/filepath"
	"testing"
)

func TestVersionComparisons(t *testing.T) {
	if c, err := CompareLlamaTag("b999999999999999999999", "b1000000000000000000000"); err != nil || c >= 0 {
		t.Fatalf("cmp=%d err=%v", c, err)
	}
	cases := []struct {
		a, b string
		want int
	}{{"v1.2.3", "1.2.4", -1}, {"1.0.0-beta.2", "1.0.0-beta.11", -1}, {"1.0.0", "1.0.0-rc.1", 1}}
	for _, tc := range cases {
		c, err := CompareSemVer(tc.a, tc.b)
		if err != nil || c != tc.want {
			t.Fatalf("%s %s: %d %v", tc.a, tc.b, c, err)
		}
	}
}
func TestStateRuntimeVersionIsLast(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	s := State{Schema: 1, LlamaTag: "b12345", Backend: "cuda-12.4", ActiveRuntime: runtimeRelative("cuda-12.4", "b12345"), Assets: []InstalledAsset{{Name: "a.zip", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	if err := ValidateState(l, s); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(RuntimePath(l, s)) != "b12345" {
		t.Fatalf("path=%s", RuntimePath(l, s))
	}
}
