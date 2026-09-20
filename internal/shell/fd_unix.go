//go:build !windows

package shell

import (
	"errors"
	"os"
	"syscall"
)

func duplicatePipe(fd int) (*os.File, error) {
	copyFD, err := syscall.Dup(fd)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(copyFD)
	f := os.NewFile(uintptr(copyFD), "lazychezmoi-reload")
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		_ = f.Close()
		return nil, errors.New("reload descriptor must be a private pipe")
	}
	if _, err := f.Write(nil); err != nil {
		_ = f.Close()
		return nil, errors.New("reload descriptor is not writable")
	}
	return f, nil
}
