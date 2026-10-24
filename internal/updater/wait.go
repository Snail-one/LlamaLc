package updater

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/procinfo"
)

func waitForLauncherExit(pid int, expected string) error {
	if pid <= 0 || expected == "" {
		return errors.New("等待退出的进程身份无效")
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		identity, alive, err := procinfo.Identity(pid)
		if err != nil {
			return err
		}
		if !alive || identity != expected {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("进程 %d 在 60 秒内没有退出，安装未执行", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForParent(pid int) error {
	identity, alive, err := procinfo.Identity(pid)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	return waitForLauncherExit(pid, identity)
}

func restoredEnvironment() []string {
	var env []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "LLAMALC_UPDATED_VERSION", "LLAMALC_UPDATE_READY_FD", "LLAMALC_UPDATE_READY_EVENT":
			continue
		}
		env = append(env, value)
	}
	return env
}
