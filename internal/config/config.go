// Package config owns lazychezmoi preferences, independently of chezmoi config.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/pelletier/go-toml/v2"
)

type Tools struct {
	Chezmoi string `toml:"chezmoi" json:"chezmoi"`
	Git     string `toml:"git" json:"git"`
}

type Config struct {
	AutoFetch bool   `toml:"auto_fetch" json:"auto_fetch"`
	Color     string `toml:"color" json:"color"`
	Tools     Tools  `toml:"tools" json:"tools"`
}

func Default() Config {
	return Config{AutoFetch: true, Color: "auto", Tools: Tools{Chezmoi: "chezmoi", Git: "git"}}
}

// Path deliberately follows XDG on macOS as well as Linux. An absolute XDG
// override works on Windows, whose fallback is the native roaming config dir.
func Path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "lazychezmoi", "config.toml"), nil
	}
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "lazychezmoi", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "lazychezmoi", "config.toml"), nil
}

func Load(path string, explicit bool) (Config, error) {
	cfg := Default()
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) && !explicit {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read lazychezmoi config %s: %w", path, err)
	}
	if err := toml.NewDecoder(bytes.NewReader(body)).DisallowUnknownFields().Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid lazychezmoi config %s: %w", path, err)
	}
	return cfg, Validate(cfg)
}

func Validate(cfg Config) error {
	if cfg.Color != "auto" && cfg.Color != "always" && cfg.Color != "never" {
		return fmt.Errorf("color must be auto, always, or never")
	}
	if cfg.Tools.Chezmoi == "" || cfg.Tools.Git == "" {
		return fmt.Errorf("tools.chezmoi and tools.git must name an executable")
	}
	return nil
}

func Encode(cfg Config) ([]byte, error) { return toml.Marshal(cfg) }

func Create(path string) error {
	body, err := Encode(Default())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create config (existing files are preserved): %w", err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
