//go:build !darwin

package sysdns

const supported = false

func snapshot() (Saved, error)      { return nil, ErrUnsupported }
func set([]string) error            { return ErrUnsupported }
func restore(Saved, []string) error { return ErrUnsupported }
