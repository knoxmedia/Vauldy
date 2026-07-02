//go:build !linux && !windows

package processctl

func Suspend(_ int) error {
	return ErrUnsupported
}

func Resume(_ int) error {
	return ErrUnsupported
}
