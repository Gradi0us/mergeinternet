//go:build !windows

package sysproxy

func platformGet() (Settings, error) { return Settings{}, ErrUnsupported }
func platformSet(Settings) error     { return ErrUnsupported }
