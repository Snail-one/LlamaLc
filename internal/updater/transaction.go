package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/managedfs"
	"github.com/Snail-one/LlamaLc/internal/update"
)

const transactionName = update.LauncherTransactionName

// Never restore executables until a failed new process is proven stopped.
type launcherStillRunningError struct{ error }

type transactionFile struct {
	Target string `json:"target"`
	Backup string `json:"backup,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after"`
}

type transaction struct {
	Schema int               `json:"schema"`
	Phase  string            `json:"phase"`
	Files  []transactionFile `json:"files"`
}

func transactionPending(root string) bool {
	_, err := os.Lstat(filepath.Join(root, "bin", transactionName))
	return !errors.Is(err, os.ErrNotExist)
}

func fileDigest(path string) (string, error) {
	info, err := stableUpdaterFileInfo(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("摘要校验时文件身份发生变化")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeTransaction(bin string, tx transaction) error {
	data, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	return managedfs.AtomicWrite(filepath.Dir(bin), filepath.Join(bin, transactionName), append(data, '\n'), 0o600)
}

func readTransaction(bin, goos string) (transaction, error) {
	path := filepath.Join(bin, transactionName)
	if err := managedfs.Validate(filepath.Dir(bin), path, false); err != nil {
		return transaction{}, err
	}
	info, err := stableUpdaterFileInfo(path)
	if err != nil {
		return transaction{}, err
	}
	if info.Size() > 8192 {
		return transaction{}, errors.New("更新恢复记录过大")
	}
	f, err := os.Open(path)
	if err != nil {
		return transaction{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return transaction{}, errors.New("更新恢复记录身份发生变化")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 8193))
	decoder.DisallowUnknownFields()
	var tx transaction
	if err := decoder.Decode(&tx); err != nil {
		return tx, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return tx, errors.New("更新恢复记录包含多余内容")
	}
	if tx.Schema != 1 || len(tx.Files) != 2 || (tx.Phase != "prepared" && tx.Phase != "committed" && tx.Phase != "rolled_back") {
		return tx, errors.New("更新恢复记录格式无效")
	}
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	for i, name := range []string{"llamaup", "llamalc"} {
		entry := tx.Files[i]
		if entry.Target != name+ext || !validDigest(entry.After) || (entry.Before != "" && !validDigest(entry.Before)) {
			return tx, errors.New("更新恢复记录目标或摘要无效")
		}
		if (entry.Backup == "") != (entry.Before == "") || (i == 0 && entry.Backup == "") {
			return tx, errors.New("更新恢复记录缺少备份")
		}
		if entry.Backup != "" {
			if err := validateStagedName(entry.Backup, "."+name+"-rollback-", "恢复备份", goos); err != nil {
				return tx, err
			}
		}
	}
	return tx, nil
}

func validDigest(value string) bool {
	return len(value) == 64 && lowerHex(value)
}

func verifyOriginal(path string, expected os.FileInfo) error {
	if expected != nil {
		return verifyUpdaterFileIdentity(path, expected)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("原本不存在的启动器路径发生变化")
	}
	return nil
}

func installTransaction(bin, updaterSource, updaterTarget, launcherSource, launcherTarget string, identities map[string]os.FileInfo, verify func() error) error {
	root := filepath.Dir(bin)
	if transactionPending(root) {
		return errors.New("存在未完成的更新，请先运行 llamaup recover")
	}
	tx := transaction{Schema: 1, Phase: "prepared"}
	for _, pair := range [][2]string{{updaterSource, updaterTarget}, {launcherSource, launcherTarget}} {
		source, target := pair[0], pair[1]
		if err := managedfs.Validate(root, source, false); err != nil {
			return err
		}
		if err := managedfs.Validate(root, target, true); err != nil {
			return err
		}
		if err := verifyUpdaterFileIdentity(source, identities[source]); err != nil {
			return err
		}
		after, err := fileDigest(source)
		if err != nil {
			return err
		}
		entry := transactionFile{Target: filepath.Base(target), After: after}
		if identities[target] != nil {
			backup, err := copyUpdateBackup(target, identities[target])
			if err != nil {
				return err
			}
			entry.Backup = filepath.Base(backup)
			entry.Before, err = fileDigest(backup)
			if err != nil {
				return err
			}
		}
		tx.Files = append(tx.Files, entry)
	}
	if err := writeTransaction(bin, tx); err != nil {
		return fmt.Errorf("保存更新恢复记录失败，未替换程序，备份保留在 bin: %w", err)
	}
	rollback := func(cause error) error {
		if err := restoreTransaction(bin, tx); err != nil {
			return fmt.Errorf("%w；回滚未完成，恢复记录和备份已保留: %v", cause, err)
		}
		return fmt.Errorf("%w；已恢复原启动器和原更新器", cause)
	}
	for _, pair := range [][2]string{{updaterSource, updaterTarget}, {launcherSource, launcherTarget}} {
		if err := verifyOriginal(pair[1], identities[pair[1]]); err != nil {
			return rollback(err)
		}
		if err := verifyUpdaterFileIdentity(pair[0], identities[pair[0]]); err != nil {
			return rollback(err)
		}
		if err := updateReplaceFile(pair[0], pair[1]); err != nil {
			return rollback(fmt.Errorf("替换程序失败: %w", err))
		}
	}
	if err := syncDirectory(bin); err != nil {
		return rollback(err)
	}
	if verify != nil {
		if err := verify(); err != nil {
			var running *launcherStillRunningError
			if errors.As(err, &running) {
				return fmt.Errorf("新版进程尚未停止，恢复记录和备份已保留: %w", err)
			}
			return rollback(fmt.Errorf("新版启动验证失败: %w", err))
		}
	}
	tx.Phase = "committed"
	if err := writeTransaction(bin, tx); err != nil {
		// The new program is already healthy and may be in use. Keep backups
		// and the journal; never roll it back while it is running.
		return fmt.Errorf("新版已就绪，但保存提交记录失败；请退出主程序后运行 llamaup recover: %w", err)
	}
	if err := cleanTransaction(bin, tx); err != nil {
		return fmt.Errorf("新版已就绪，清理待重试: %w", err)
	}
	return nil
}

func restoreTransaction(bin string, tx transaction) error {
	root := filepath.Dir(bin)
	// Validate every path and backup before changing either target. Recovery is
	// repeatable: backups are copied, not consumed by restoration.
	for _, entry := range tx.Files {
		target := filepath.Join(bin, entry.Target)
		if err := managedfs.Validate(root, target, true); err != nil {
			return err
		}
		current, err := fileDigest(target)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && current != entry.Before && current != entry.After {
			return fmt.Errorf("程序在更新后被修改，拒绝覆盖: %s", target)
		}
		if entry.Backup != "" {
			backup := filepath.Join(bin, entry.Backup)
			if err := managedfs.Validate(root, backup, false); err != nil {
				return err
			}
			digest, err := fileDigest(backup)
			if err != nil || digest != entry.Before {
				return fmt.Errorf("恢复备份校验失败: %s", backup)
			}
		}
	}
	for _, entry := range tx.Files {
		target := filepath.Join(bin, entry.Target)
		if entry.Backup == "" {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		backup := filepath.Join(bin, entry.Backup)
		info, err := stableUpdaterFileInfo(backup)
		if err != nil {
			return err
		}
		// Reuse the strict staging filename grammar so interrupted restoration
		// leaves only recognizable managed files.
		temp := filepath.Join(bin, strings.Replace(entry.Backup, "-rollback-", "-new-", 1))
		if err := managedfs.Validate(root, temp, true); err != nil {
			return err
		}
		if _, err := os.Lstat(temp); err == nil {
			digest, err := fileDigest(temp)
			if err != nil || digest != entry.Before {
				return fmt.Errorf("恢复暂存路径已存在且内容不匹配: %s", temp)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else if err := managedfs.CopyFile(root, backup, temp, info.Mode().Perm()); err != nil {
			return err
		}
		if err := updateReplaceFile(temp, target); err != nil {
			_ = os.Remove(temp)
			return err
		}
	}
	if err := syncDirectory(bin); err != nil {
		return err
	}
	tx.Phase = "rolled_back"
	if err := writeTransaction(bin, tx); err != nil {
		return err
	}
	return cleanTransaction(bin, tx)
}

func cleanTransaction(bin string, tx transaction) error {
	for _, entry := range tx.Files {
		if entry.Backup == "" {
			continue
		}
		path := filepath.Join(bin, entry.Backup)
		if err := managedfs.Validate(filepath.Dir(bin), path, true); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := syncDirectory(bin); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(bin, transactionName)); err != nil {
		return err
	}
	return syncDirectory(bin)
}

func recoverTransaction(root, goos string) error {
	bin := filepath.Join(root, "bin")
	tx, err := readTransaction(bin, goos)
	if err != nil {
		return err
	}
	if tx.Phase == "prepared" {
		return restoreTransaction(bin, tx)
	}
	for _, entry := range tx.Files {
		want := entry.After
		if tx.Phase == "rolled_back" {
			want = entry.Before
		}
		digest, err := fileDigest(filepath.Join(bin, entry.Target))
		if want == "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || digest != want {
			return errors.New("恢复记录与当前程序不匹配，保留备份以供人工恢复")
		}
	}
	return cleanTransaction(bin, tx)
}
