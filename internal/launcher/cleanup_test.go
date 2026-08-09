package launcher

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ageForAutomaticCleanup(t *testing.T, path string) {
	t.Helper()
	old := cleanupClock().Add(-automaticCleanupMinAge - time.Hour)
	var paths []string
	if err := filepath.Walk(path, func(current string, _ os.FileInfo, err error) error {
		if err == nil {
			paths = append(paths, current)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := os.Chtimes(paths[index], old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMainMenuUsesDForCleanupAndRecovery(t *testing.T) {
	root := t.TempDir()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("3", "3", "q", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	for _, want := range []string{"升级维护", "[3] 清理与恢复", "清理与恢复", "未发现需要处理的残留或恢复目录"} {
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
	ageForAutomaticCleanup(t, owned)
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

func TestFreshOwnedTempIsProtectedFromCleanup(t *testing.T) {
	root := t.TempDir()
	base := managedRuntimeRoot(root)
	target := filepath.Join(base, ".staging-12345")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := markManagedTempDirectory(base, target); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(target, "active-download"))
	cleanupRuntimeTemps(root, io.Discard)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("fresh owned temp was automatically removed: %v", err)
	}
	candidates, _ := scanCleanupCandidates(root)
	if len(candidates) != 1 || candidates[0].Automatic || !candidates[0].Recent {
		t.Fatalf("fresh temp classification=%#v", candidates)
	}
	if err := deleteCleanupCandidate(root, candidates[0], false); err == nil || !strings.Contains(err.Error(), "可能仍在使用") {
		t.Fatalf("fresh temp manual deletion result=%v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("fresh temp changed after refused deletion: %v", err)
	}
}

func TestCleanupMenuDisplaysCandidateSummary(t *testing.T) {
	root := t.TempDir()
	base := managedRuntimeRoot(root)
	target := filepath.Join(base, ".staging-12345")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := markManagedTempDirectory(base, target); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(target, "active-download"))
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("3", "3", "0", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut)
	}
	for _, want := range []string{
		"发现 1 项：可安全清理 0，需确认 0，暂不处理 1。",
		"[1] 清理全部安全项（当前无可清理项）", "待处理项目", "[2] 近期运行时下载暂存", "状态: 暂不处理（可能正在使用）",
		"大小:", "路径: " + target, "说明: 最近 24 小时内创建或修改",
		"操作", "[0/q] 返回主菜单",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("cleanup output missing %q:\n%s", want, out)
		}
	}
	cleanupStart := strings.Index(out.String(), "清理与恢复\n"+menuRule)
	if cleanupStart < 0 {
		t.Fatalf("cleanup menu boundaries missing:\n%s", out)
	}
	cleanupEnd := strings.Index(out.String()[cleanupStart:], "请选择操作或项目编号:")
	if cleanupEnd < 0 {
		t.Fatalf("cleanup menu boundaries missing:\n%s", out)
	}
	cleanupOutput := out.String()[cleanupStart : cleanupStart+cleanupEnd]
	if strings.Count(cleanupOutput, "返回主菜单") != 1 {
		t.Fatalf("cleanup menu return action was not combined:\n%s", cleanupOutput)
	}
}

func TestCleanupMenuUsesOneForBatchCleanup(t *testing.T) {
	root := t.TempDir()
	base := managedRuntimeRoot(root)
	target := filepath.Join(base, ".staging-12345")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := markManagedTempDirectory(base, target); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(target, "completed-download"))
	ageForAutomaticCleanup(t, target)
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("3", "3", "1", "0", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("numeric batch cleanup did not remove safe target: %v", err)
	}
	if !strings.Contains(out.String(), "[1] 清理全部安全项（1 项）") || !strings.Contains(out.String(), "已清理: "+target) {
		t.Fatalf("numeric batch cleanup output missing:\n%s", out)
	}
}

func TestCleanupMenuDisablesDeletionForRecentItem(t *testing.T) {
	root := t.TempDir()
	base := managedRuntimeRoot(root)
	target := filepath.Join(base, ".staging-12345")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := markManagedTempDirectory(base, target); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(target, "active-download"))
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Stdin: menuInput("3", "3", "2", "3", "q", "q"),
		Stdout: out, Stderr: errOut, Executor: &fakeExecutor{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut)
	}
	if !strings.Contains(out.String(), "[3] 永久删除（当前不可用）") || !strings.Contains(out.String(), "当前不允许删除") {
		t.Fatalf("recent cleanup item did not disable deletion:\n%s", out)
	}
	if count := strings.Count(out.String(), "清理与恢复\n"+menuRule); count != 1 {
		t.Fatalf("cleanup list repeated after unavailable action %d times:\n%s", count, out)
	}
	if count := strings.Count(out.String(), "项目操作"); count != 2 {
		t.Fatalf("candidate actions were not redisplayed in place, count=%d:\n%s", count, out)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("recent cleanup item was changed: %v", err)
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
		Root: root, Config: DefaultConfig(), Stdin: menuInput("3", "3", "2", "3", "y", "q", "q"),
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
		Root: root, Config: DefaultConfig(), Stdin: menuInput("3", "3", "2", "3", "n", "q", "q"),
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
		Root: root, Config: DefaultConfig(), Stdin: menuInput("3", "3", "2", "2", "q", "q"),
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
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(target, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deleteCleanupCandidate(root, candidates[0], false); err == nil {
		t.Fatal("changed target was deleted")
	}
	if content, err := os.ReadFile(replacement); err != nil || string(content) != "replacement" {
		t.Fatalf("replacement target changed: %q err=%v", content, err)
	}
}

func TestRegisteredPendingCleanupIsVisibleAndUpdatesState(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(managedRuntimeRoot(root), "b2-cpu")
	pending := filepath.Join(managedRuntimeRoot(root), "b1-cpu")
	for _, path := range []string{active, pending} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touchFile(t, filepath.Join(active, "llama-server"))
	touchFile(t, filepath.Join(pending, "old-runtime"))
	ageForAutomaticCleanup(t, pending)
	state := UpdateState{
		Schema: 1, LlamaTag: "b2", Backend: "cpu", ActiveRuntime: "data/llama.cpp/b2-cpu",
		Assets:         []InstalledAsset{{Name: "runtime.tar.gz", SHA256: testDigest}},
		PendingCleanup: []string{"data/llama.cpp/b1-cpu"},
	}
	if err := WriteUpdateState(root, state); err != nil {
		t.Fatal(err)
	}
	candidates, _ := scanCleanupCandidates(root)
	var selected *cleanupCandidate
	for index := range candidates {
		if candidates[index].Path == pending {
			selected = &candidates[index]
			break
		}
	}
	if selected == nil || !selected.Automatic || selected.Kind != "已登记待清理运行时" {
		t.Fatalf("pending cleanup classification=%#v candidates=%#v", selected, candidates)
	}
	if err := deleteCleanupCandidate(root, *selected, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending runtime was not deleted: %v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active runtime was changed: %v", err)
	}
	updated, exists, err := LoadUpdateState(root)
	if err != nil || !exists || len(updated.PendingCleanup) != 0 {
		t.Fatalf("pending state was not cleared: %#v exists=%v err=%v", updated, exists, err)
	}
}

func TestCleanupRefusesCandidateContainingSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(managedRuntimeRoot(root), "user-runtime")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "important")
	if err := os.WriteFile(external, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(target, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	candidates, _ := scanCleanupCandidates(root)
	if len(candidates) != 1 {
		t.Fatalf("candidates=%#v", candidates)
	}
	if err := deleteCleanupCandidate(root, candidates[0], false); err == nil || !strings.Contains(err.Error(), "安全遍历") {
		t.Fatalf("symlink candidate deletion result=%v", err)
	}
	if content, err := os.ReadFile(external); err != nil || string(content) != "important" {
		t.Fatalf("symlink target changed: %q err=%v", content, err)
	}
}

func TestRecoveryMetadataReadIsSizeLimited(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, "data", "llama.cpp-recovery")
	if err := os.MkdirAll(recovery, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recovery, ".llamalc-recovery.json"), bytes.Repeat([]byte("x"), (64<<10)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	candidates, _ := scanCleanupCandidates(root)
	if len(candidates) != 1 || !strings.Contains(candidates[0].Reason, "没有可用元数据") {
		t.Fatalf("oversized metadata classification=%#v", candidates)
	}
}

func TestRecoveryMetadataIsEscapedForTerminalOutput(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, "data", "llama.cpp-recovery")
	if err := os.MkdirAll(recovery, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"schema":1,"created_at":"2026-07-15T00:00:00Z","original_path":"x","reason":"unsafe\u001b[31m\nline"}`
	if err := os.WriteFile(filepath.Join(recovery, ".llamalc-recovery.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	candidates, _ := scanCleanupCandidates(root)
	if len(candidates) != 1 {
		t.Fatalf("candidates=%#v", candidates)
	}
	if strings.ContainsRune(candidates[0].Reason, '\x1b') || strings.Contains(candidates[0].Reason, "\nline") {
		t.Fatalf("metadata control characters were not escaped: %q", candidates[0].Reason)
	}
	if !strings.Contains(candidates[0].Reason, `\u001B[31m\nline`) {
		t.Fatalf("escaped metadata reason missing: %q", candidates[0].Reason)
	}
}
