//go:build !windows

package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/release"
	"github.com/Snail-one/LlamaLc/internal/update"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

const integrationTrailer = "\nLLAMALC_INTEGRATION_NEW\n"

// Running a copy of this test executable gives the integration test real
// launcher/updater/installer processes without network access or nested builds.
func TestMain(m *testing.M) {
	if root := os.Getenv("LLAMALC_TEST_DEPLOYMENT"); root != "" {
		os.Exit(integrationProcess(root))
	}
	os.Exit(m.Run())
}

func integrationProcess(root string) int {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	image, err := os.ReadFile(exe)
	if err != nil {
		panic(err)
	}
	buildversion.Version = "v1.0.0"
	if bytes.HasSuffix(image, []byte(integrationTrailer)) {
		buildversion.Version = "v999.0.0"
	}
	l, err := layout.New(root, runtime.GOOS)
	if err != nil {
		panic(err)
	}
	if len(os.Args) == 2 && os.Args[1] == "delegate" {
		outcome, err := RunExternal(context.Background(), l, update.LauncherOptions{}, true, os.Stdin, os.Stdout, os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 91
		}
		if !outcome.Handoff {
			fmt.Fprintln(os.Stderr, "missing explicit handoff")
			return 92
		}
		fmt.Fprintln(os.Stdout, "MAIN_EXIT_FOR_INSTALL")
		return 0
	}
	if len(os.Args) == 1 && filepath.Base(exe) == "llamalc" {
		if buildversion.Version == "v999.0.0" && os.Getenv("LLAMALC_TEST_FAIL_NEW") == "1" {
			fmt.Fprintln(os.Stdout, "HELPER_NEW_CRASH")
			return 42
		}
		if os.Getenv("LLAMALC_UPDATE_READY_FD") != "3" {
			fmt.Fprintln(os.Stdout, "HELPER_MAIN_READY", buildversion.Version)
			return 0
		}
		pipe := os.NewFile(3, "ready")
		if _, err := fmt.Fprintln(pipe, "Version: "+buildversion.Version); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 93
		}
		if err := pipe.Close(); err != nil {
			return 94
		}
		fmt.Fprintln(os.Stdout, "HELPER_MAIN_READY", buildversion.Version)
		return 0
	}
	newWorkflowManager = func(l layout.Layout, out, stderr io.Writer) *update.Manager {
		metadata, err := os.ReadFile(filepath.Join(root, "fixtures", "release.json"))
		if err != nil {
			panic(err)
		}
		var rel release.GitHubRelease
		if err := json.Unmarshal(metadata, &rel); err != nil {
			panic(err)
		}
		files := map[string][]byte{}
		for _, asset := range rel.Assets {
			data, err := os.ReadFile(filepath.Join(root, "fixtures", asset.Name))
			if err != nil {
				panic(err)
			}
			files[asset.Name] = data
		}
		manager := update.NewManager(l, &workflowSource{release: rel, files: files})
		manager.Out, manager.Err = out, stderr
		return manager
	}
	return Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

func TestIndependentUpdaterProcessLifecycle(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	oldImage, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	newImage := append(append([]byte{}, oldImage...), []byte(integrationTrailer)...)
	var bundle bytes.Buffer
	gz := gzip.NewWriter(&bundle)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"llamalc", "llamaup"} {
		if err := tw.WriteHeader(&tar.Header{Name: "LlamaLc/bin/" + name, Mode: 0o755, Size: int64(len(newImage)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(newImage); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	name := "llamalc-" + runtime.GOOS + "-" + runtime.GOARCH + "-v999.0.0.tar.gz"
	archiveHash := sha256.Sum256(bundle.Bytes())
	sums := []byte(hex.EncodeToString(archiveHash[:]) + "  " + name + "\n")
	sumsHash := sha256.Sum256(sums)
	metadata, err := json.Marshal(release.GitHubRelease{Tag: "v999.0.0", Assets: []release.Asset{
		{Name: name, Size: int64(bundle.Len()), Digest: "sha256:" + hex.EncodeToString(archiveHash[:])},
		{Name: "SHA256SUMS.txt", Size: int64(len(sums)), Digest: "sha256:" + hex.EncodeToString(sumsHash[:])},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, failNew := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[failNew], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "LlamaLc")
			bin := filepath.Join(root, "bin")
			fixtures := filepath.Join(root, "fixtures")
			for _, dir := range []string{bin, fixtures} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for path, data := range map[string][]byte{
				filepath.Join(bin, "llamalc"): oldImage, filepath.Join(bin, "llamaup"): oldImage,
				filepath.Join(fixtures, name): bundle.Bytes(), filepath.Join(fixtures, "SHA256SUMS.txt"): sums, filepath.Join(fixtures, "release.json"): metadata,
			} {
				if err := os.WriteFile(path, data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(bin, "llamalc"), "delegate")
			cmd.Env = append(os.Environ(), "LLAMALC_TEST_DEPLOYMENT="+root)
			if failNew {
				cmd.Env = append(cmd.Env, "LLAMALC_TEST_FAIL_NEW=1")
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			cmd.WaitDelay = 3 * time.Second
			if err := cmd.Run(); err != nil {
				t.Fatalf("lifecycle: %v\n%s", err, &output)
			}
			text := output.String()
			for _, want := range []string{"MAIN_EXIT_FOR_INSTALL", "启动器更新计划", "文件替换完成"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q:\n%s", want, text)
				}
			}
			wantImage := newImage
			if failNew {
				wantImage = oldImage
				for _, want := range []string{"HELPER_NEW_CRASH", "已恢复原启动器和原更新器", "HELPER_MAIN_READY v1.0.0"} {
					if !strings.Contains(text, want) {
						t.Fatalf("missing %q:\n%s", want, text)
					}
				}
				if strings.Contains(text, "更新完成：") {
					t.Fatalf("failed launch reported success:\n%s", text)
				}
			} else {
				for _, want := range []string{"HELPER_MAIN_READY v999.0.0", "新版已就绪"} {
					if !strings.Contains(text, want) {
						t.Fatalf("missing %q:\n%s", want, text)
					}
				}
			}
			for _, name := range []string{"llamalc", "llamaup"} {
				data, err := os.ReadFile(filepath.Join(bin, name))
				if err != nil || !bytes.Equal(data, wantImage) {
					t.Fatalf("%s wrong after lifecycle: %v", name, err)
				}
			}
			if transactionPending(root) {
				t.Fatalf("journal left after lifecycle:\n%s", text)
			}
			backups, _ := filepath.Glob(filepath.Join(bin, ".*-rollback-*"))
			if len(backups) > 0 {
				t.Fatalf("backups left: %v", backups)
			}
		})
	}
	t.Run("recover", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "LlamaLc")
		bin := filepath.Join(root, "bin")
		fixtures := filepath.Join(root, "fixtures")
		for _, dir := range []string{bin, fixtures} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		launcherName, updaterName := ".llamalc-new-0123456789abcdef", ".llamaup-new-0123456789abcdef"
		for path, data := range map[string][]byte{
			filepath.Join(bin, "llamalc"): oldImage, filepath.Join(bin, "llamaup"): oldImage,
			filepath.Join(bin, launcherName): newImage, filepath.Join(bin, updaterName): newImage,
			filepath.Join(fixtures, name): bundle.Bytes(), filepath.Join(fixtures, "SHA256SUMS.txt"): sums, filepath.Join(fixtures, "release.json"): metadata,
		} {
			if err := os.WriteFile(path, data, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		err := applyUpdateWithValidation(root, runtime.GOOS, launcherName, updaterName, func() error { return &launcherStillRunningError{fmt.Errorf("simulated interrupted commit")} })
		if err == nil || !transactionPending(root) {
			t.Fatalf("missing interrupted transaction: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(bin, "llamaup"), "recover", "--yes")
		cmd.Env = append(os.Environ(), "LLAMALC_TEST_DEPLOYMENT="+root)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		cmd.WaitDelay = 3 * time.Second
		if err := cmd.Run(); err != nil {
			t.Fatalf("recovery: %v\n%s", err, &output)
		}
		for _, want := range []string{"中断的更新已恢复", "HELPER_MAIN_READY v1.0.0"} {
			if !strings.Contains(output.String(), want) {
				t.Fatalf("missing %q:\n%s", want, &output)
			}
		}
		for _, name := range []string{"llamalc", "llamaup"} {
			data, err := os.ReadFile(filepath.Join(bin, name))
			if err != nil || !bytes.Equal(data, oldImage) {
				t.Fatalf("%s not restored: %v", name, err)
			}
		}
		if transactionPending(root) {
			t.Fatal("recovery left journal")
		}
	})

}
