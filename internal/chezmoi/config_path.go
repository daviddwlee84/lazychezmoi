package chezmoi

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// This locates the filename only; native dump-config still validates config.
// It mirrors chezmoi's defaultConfigFile and go-xdg/v6 search order, including
// ~/.config on Windows and its supported .yml alias. No source tree is scanned.
func chezmoiConfigFile(explicit string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return "", err
	}
	return locateChezmoiConfig(explicit, home, os.Getenv)
}

func nativeAbsolutePath(value, home string) (string, error) {
	value = filepath.Clean(value)
	switch {
	case value == "~":
		value = home
	case strings.HasPrefix(value, "~/") || runtime.GOOS == "windows" && strings.HasPrefix(value, `~\`):
		value = filepath.Join(home, value[2:])
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(path)
		path = strings.ToUpper(volume) + path[len(volume):]
	}
	return filepath.ToSlash(path), nil
}

func locateChezmoiConfig(explicit, home string, getenv func(string) string) (string, error) {
	if explicit != "" {
		return nativeAbsolutePath(explicit, home)
	}
	configHome := getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	configDirs := getenv("XDG_CONFIG_DIRS")
	if configDirs == "" {
		configDirs = filepath.Join(string(filepath.Separator), "etc", "xdg")
	}
	dirs := append([]string{configHome}, filepath.SplitList(configDirs)...)
	for _, dir := range dirs {
		absolute, err := nativeAbsolutePath(dir, home)
		if err != nil {
			return "", err
		}
		path := filepath.Join(absolute, "chezmoi")
		entries, err := os.ReadDir(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		names := make(map[string]bool, len(entries))
		for _, entry := range entries {
			names[entry.Name()] = true
		}
		var matches []string
		for _, extension := range []string{"json", "jsonc", "toml", "yaml", "yml"} {
			if names["chezmoi."+extension] {
				matches = append(matches, filepath.ToSlash(filepath.Join(path, "chezmoi."+extension)))
			}
		}
		switch len(matches) {
		case 0:
			continue
		case 1:
			return matches[0], nil
		default:
			return "", fmt.Errorf("multiple chezmoi config files: %s", strings.Join(matches, ", "))
		}
	}
	absolute, err := nativeAbsolutePath(configHome, home)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(absolute, "chezmoi", "chezmoi.toml")), nil
}
