//go:build !linux && !windows && !aix && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !solaris

package procinfo

func platformIdentity(int) (string, bool, error) {
	return "", false, ErrUnsupported
}
