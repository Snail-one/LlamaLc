//go:build !windows

package launcher

func waitForUpdaterParent(_ int) error { return nil }
