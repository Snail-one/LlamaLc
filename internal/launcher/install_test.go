package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyInstallationFailures(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
		want   string
	}{
		{name: "spawn error", err: errors.New("exec format error"), want: "exec format error"},
		{name: "empty output", output: " ", want: "输出无法识别"},
		{name: "missing compiler", output: "version: 1234\n", want: "输出无法识别"},
		{name: "missing version", output: "built with cc\n", want: "输出无法识别"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			paths, err := ResolveFixedPaths(root, "linux")
			if err != nil {
				t.Fatal(err)
			}
			touchFile(t, paths.Server)
			probe := &fakeInstallationProbe{output: test.output, err: test.err}
			if _, err := VerifyInstallation(root, paths, probe); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOSInstallationProbeCapturesOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	output, err := (OSInstallationProbe{}).Probe(Command{
		Path: executable,
		Args: []string{"-test.run=^TestInstallationProbeHelperProcess$"},
		Dir:  t.TempDir(),
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "version: helper") || !strings.Contains(output, "built with helper") {
		t.Fatalf("combined output was not captured: %q", output)
	}
}

func TestOSInstallationProbeTimeout(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = (OSInstallationProbe{}).Probe(Command{
		Path: executable,
		Args: []string{"-test.run=^TestInstallationProbeSleepHelperProcess$"},
		Dir:  t.TempDir(),
	}, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "探测超时") {
		t.Fatalf("probe timeout was not reported: %v", err)
	}
}

func TestInstallationProbeHelperProcess(t *testing.T) {
	if !hasExactTestRunArgument("TestInstallationProbeHelperProcess") {
		return
	}
	fmt.Fprintln(os.Stdout, "version: helper")
	fmt.Fprintln(os.Stderr, "built with helper")
}

func TestInstallationProbeSleepHelperProcess(t *testing.T) {
	if !hasExactTestRunArgument("TestInstallationProbeSleepHelperProcess") {
		return
	}
	time.Sleep(time.Second)
}

func hasExactTestRunArgument(name string) bool {
	want := "-test.run=^" + name + "$"
	for _, arg := range os.Args[1:] {
		if arg == want {
			return true
		}
	}
	return false
}

func TestVerifyInstallationAcceptsCaseInsensitiveSignature(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolveFixedPaths(root, "linux")
	if err != nil {
		t.Fatal(err)
	}
	touchFile(t, paths.Server)
	probe := &fakeInstallationProbe{output: "backend initialization\nVERSION: 1234 (abc)\nBUILT WITH GCC for Linux\n"}
	summary, err := VerifyInstallation(root, paths, probe)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "VERSION: 1234 (abc)" || len(probe.commands) != 1 || probe.timeouts[0] != 30*time.Second {
		t.Fatalf("unexpected probe result: summary=%q commands=%#v timeouts=%#v", summary, probe.commands, probe.timeouts)
	}
}

func TestVerifyInstallationRequiresRegularServerFile(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolveFixedPaths(root, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.Server, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := &fakeInstallationProbe{}
	if _, err := VerifyInstallation(root, paths, probe); err == nil || !strings.Contains(err.Error(), "路径是目录") {
		t.Fatalf("server directory was accepted: %v", err)
	}
	if len(probe.commands) != 0 {
		t.Fatal("invalid server file was executed")
	}
	if _, err := os.Stat(filepath.Join(root, "config")); !os.IsNotExist(err) {
		t.Fatalf("installation verification created config: %v", err)
	}
}

func TestCappedProbeOutputDoesNotGrowPastLimit(t *testing.T) {
	output := &cappedOutput{limit: 16}
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	n, err := output.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("unexpected write result: n=%d err=%v", n, err)
	}
	if !output.Exceeded() || len(output.String()) != 16 {
		t.Fatalf("output limit failed: exceeded=%v length=%d", output.Exceeded(), len(output.String()))
	}
}
