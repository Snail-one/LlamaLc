package launcher

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeExecutor struct {
	commands []Command
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	code     int
	err      error
}

func (f *fakeExecutor) Execute(command Command, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	f.commands = append(f.commands, command)
	f.stdin, f.stdout, f.stderr = stdin, stdout, stderr
	return f.code, f.err
}

func TestMainFlagOverridesConfigAndForwardsStreams(t *testing.T) {
	root := t.TempDir()
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
	if !strings.Contains(out.String(), Version) {
		t.Fatalf("menu header does not contain version %q: %s", Version, out.String())
	}
}

func TestVersionFlagOnlyPrintsVersion(t *testing.T) {
	oldVersion := Version
	Version = "v9.8.7-test"
	t.Cleanup(func() { Version = oldVersion })

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	fake := &fakeExecutor{}
	code := Main([]string{"-v"}, bytes.NewBuffer(nil), out, errOut, fake)
	if code != 0 {
		t.Fatalf("version returned %d: %s", code, errOut.String())
	}
	if out.String() != "v9.8.7-test\n" {
		t.Fatalf("unexpected version output: %q", out.String())
	}
	if errOut.Len() != 0 || len(fake.commands) != 0 {
		t.Fatalf("version inspection had side effects: stderr=%q commands=%#v", errOut.String(), fake.commands)
	}
}
