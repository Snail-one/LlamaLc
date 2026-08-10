package update

import (
	"context"
	"errors"
	"fmt"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
)

// ErrRuntimeNotInstalled distinguishes a clean first installation from a
// damaged or unverifiable managed runtime.
var ErrRuntimeNotInstalled = errors.New("llama.cpp 尚未安装")

// ActiveRuntime is the single authoritative result used before starting a
// service and after installing a runtime.
type ActiveRuntime struct {
	State    State
	Runtime  llama.Runtime
	Detected string
}

func (r ActiveRuntime) Display() string {
	return r.State.LlamaTag + " / " + r.State.Backend + " — " + r.Detected
}

// ValidateActiveRuntime loads the managed state, locates exactly one server
// and CLI, executes the server version probe, and verifies the reported build
// against the registered llama.cpp tag.
func ValidateActiveRuntime(ctx context.Context, l layout.Layout, goos string) (ActiveRuntime, error) {
	state, exists, err := LoadState(l)
	if err != nil {
		return ActiveRuntime{}, err
	}
	if !exists || state.ActiveRuntime == "" {
		return ActiveRuntime{}, ErrRuntimeNotInstalled
	}
	runtime, err := llama.Locate(RuntimePath(l, state), goos)
	if err != nil {
		return ActiveRuntime{}, fmt.Errorf("校验活动 llama.cpp 运行时: %w", err)
	}
	detected, err := llama.ProbeVersion(ctx, runtime.Server)
	if err != nil {
		return ActiveRuntime{}, err
	}
	if !matchesLlamaTag(detected, state.LlamaTag) {
		return ActiveRuntime{}, fmt.Errorf("活动运行时版本与登记 tag %s 不匹配: %s", state.LlamaTag, detected)
	}
	runtime.Version = detected
	return ActiveRuntime{State: state, Runtime: runtime, Detected: detected}, nil
}
