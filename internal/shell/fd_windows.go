package shell

import (
	"errors"
	"os"
)

func duplicatePipe(_ int) (*os.File, error) {
	return nil, errors.New("Windows shell reload requires the PowerShell wrapper")
}
