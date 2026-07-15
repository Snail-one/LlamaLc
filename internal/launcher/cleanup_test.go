package launcher

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainMenuUsesDForCleanupAndRecovery(t *testing.T) {
	root := t.TempDir()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("d", "q", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	for _, want := range []string{"d. 清理与恢复", "清理与恢复", "未发现需要处理的残留或恢复目录"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("menu output missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "10. 清理与恢复") {
		t.Fatalf("cleanup was exposed as numeric option: %s", out.String())
	}
}

func TestScanCleanupCandidatesClassifiesOwnedAndReviewItems(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	base := managedRuntimeRoot(root)
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(base, ".staging-12345")
	if err := os.Mkdir(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := markManagedTempDirectory(base, owned); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(base, "user-runtime")
	if err := os.Mkdir(untracked, 0o755); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(untracked, "keep.txt"))
	recovery := filepath.Join(data, "llama.cpp-recovery")
	if err := os.Mkdir(recovery, 0o755); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(recovery, "old-file"))

	candidates, _ := scanCleanupCandidates(root)
	byPath := make(map[string]cleanupCandidate)
	for _, candidate := range candidates {
		byPath[candidate.Path] = candidate
	}
	if candidate, ok := byPath[owned]; !ok || !candidate.Automatic || candidate.Kind != "运行时下载暂存" {
		t.Fatalf("owned staging classification=%#v exists=%v", candidate, ok)
	}
	if candidate, ok := byPath[untracked]; !ok || candidate.Automatic || candidate.Kind != "未登记运行时目录" {
		t.Fatalf("untracked classification=%#v exists=%v", candidate, ok)
	}
	if candidate, ok := byPath[recovery]; !ok || candidate.Automatic || candidate.Kind != "恢复备份" {
		t.Fatalf("recovery classification=%#v exists=%v", candidate, ok)
	}
}

func TestCleanupMenuDeletesReviewItemOnlyAfterConfirmation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(managedRuntimeRoot(root), "user-runtime")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(marker, []byte("review me"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("d", "1", "d", "y", "q", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed review target was not deleted: %v", err)
	}
	for _, want := range []string{target, "即将永久删除完整路径", "确认已检查并转移需要保留的文件", "已删除"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("delete output missing %q: %s", want, out.String())
		}
	}
}

func TestCleanupMenuCancelPreservesReviewItem(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(managedRuntimeRoot(root), "user-runtime")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(target, "keep.txt"))
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("d", "1", "d", "n", "q", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("cancelled review target was changed: %v", err)
	}
	if !strings.Contains(out.String(), "已取消，未修改任何文件") {
		t.Fatalf("cancel result missing: %s", out.String())
	}
}

func TestCleanupMenuCanOpenSelectedDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(managedRuntimeRoot(root), "user-runtime")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	original := launchCleanupPath
	var opened string
	launchCleanupPath = func(path string) error {
		opened = path
		return nil
	}
	t.Cleanup(func() { launchCleanupPath = original })
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("d", "1", "o", "q", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	if opened != target {
		t.Fatalf("opened path=%q want=%q", opened, target)
	}
}

func TestDeleteCleanupCandidateRefusesChangedTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(managedRuntimeRoot(root), "user-runtime")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	candidates, _ := scanCleanupCandidates(root)
	if len(candidates) != 1 {
		t.Fatalf("candidates=%#v", candidates)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deleteCleanupCandidate(root, candidates[0], false); err == nil {
		t.Fatal("changed target was deleted")
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "replacement" {
		t.Fatalf("replacement target changed: %q err=%v", content, err)
	}
}
