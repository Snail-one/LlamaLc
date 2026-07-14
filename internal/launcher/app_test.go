package launcher

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

type fakeExecutor struct {
	commands []Command
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	code     int
	err      error
}

func mockExecutableInBin(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "bin", "llama-launcher.exe")
	touchFile(t, path)
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })
}

func (f *fakeExecutor) Execute(command Command, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	f.commands = append(f.commands, command)
	f.stdin, f.stdout, f.stderr = stdin, stdout, stderr
	return f.code, f.err
}

func TestMainFlagOverridesConfigAndForwardsStreams(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	touchFile(t, filepath.Join(root, "models", "chat.gguf"))
	config := `{"server":{"host":"config-host","port":30001,"n_gpu_layers":"0"}}`
	if err := os.WriteFile(filepath.Join(root, DefaultConfigName), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("stdin")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	fake := &fakeExecutor{code: 23}
	code := Main([]string{"--root", root, "serve", "--model", "chat.gguf", "--port", "30002", "--", "--threads", "8"}, in, out, errOut, fake)
	if code != 23 || len(fake.commands) != 1 {
		t.Fatalf("unexpected result: code=%d calls=%d stderr=%s", code, len(fake.commands), errOut.String())
	}
	wantTail := []string{"--host", "config-host", "--port", "30002", "--no-ui", "--threads", "8"}
	args := fake.commands[0].Args
	if !reflect.DeepEqual(args[len(args)-len(wantTail):], wantTail) {
		t.Fatalf("flags/config/extra precedence failed: %#v", args)
	}
	if fake.stdin != in || fake.stdout != out || fake.stderr != errOut {
		t.Fatal("standard streams were not connected to executor")
	}
}

func TestMenuCancellationDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "models", "chat.gguf"))
	in := bytes.NewBufferString("1\n1\nn\n0\n")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	fake := &fakeExecutor{}
	code := Main([]string{"--root", root}, in, out, errOut, fake)
	if code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	if len(fake.commands) != 0 {
		t.Fatalf("cancelled launch executed process: %#v", fake.commands)
	}
	if !strings.Contains(out.String(), buildversion.Version) {
		t.Fatalf("menu header does not contain version %q: %s", buildversion.Version, out.String())
	}
}

func TestVersionCommandsOnlyPrintVersion(t *testing.T) {
	for _, versionFlag := range []string{"-v", "--version", "version"} {
		t.Run(versionFlag, func(t *testing.T) {
			oldVersion, oldCommit, oldBuildDate := buildversion.Version, buildversion.Commit, buildversion.BuildDate
			buildversion.Version = "v9.8.7-test"
			buildversion.Commit = "abc1234"
			buildversion.BuildDate = "2026-07-14T12:00:00Z"
			t.Cleanup(func() {
				buildversion.Version, buildversion.Commit, buildversion.BuildDate = oldVersion, oldCommit, oldBuildDate
			})

			out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
			fake := &fakeExecutor{}
			code := Main([]string{versionFlag}, bytes.NewBuffer(nil), out, errOut, fake)
			if code != 0 {
				t.Fatalf("version returned %d: %s", code, errOut.String())
			}
			want := "Version:   v9.8.7-test\nCommit:    abc1234\nBuildDate: 2026-07-14T12:00:00Z\n"
			if out.String() != want {
				t.Fatalf("unexpected version output: %q", out.String())
			}
			if errOut.Len() != 0 || len(fake.commands) != 0 {
				t.Fatalf("version inspection had side effects: stderr=%q commands=%#v", errOut.String(), fake.commands)
			}
		})
	}
}

func TestMainRejectsExecutableOutsideBin(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "llama-launcher.exe")
	touchFile(t, path)
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	code := Main(nil, bytes.NewBuffer(nil), out, errOut, &fakeExecutor{})
	if code != 1 || !strings.Contains(errOut.String(), "必须放在 bin 目录下") {
		t.Fatalf("outside-bin executable was not rejected: code=%d stderr=%q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, DefaultConfigName)); !os.IsNotExist(err) {
		t.Fatalf("location validation should happen before config creation, stat error: %v", err)
	}

}
