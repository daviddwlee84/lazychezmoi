package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPreservesExplicitFalseAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("auto_fetch = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoFetch || cfg.Color != "auto" || cfg.Tools.Chezmoi != "chezmoi" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestMissingExplicitAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")
	if _, err := Load(path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("explicit missing config accepted")
	}
	if err := os.WriteFile(path, []byte("auto_ftech = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("typo accepted")
	}
}

func TestConfigPathHonorsAbsoluteXDGOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "lazychezmoi", "config.toml") {
		t.Fatal(got)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	got, err = Path()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("relative XDG leaked into result: %s", got)
	}
}

func TestCreateDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if err := Create(path); err == nil {
		t.Fatal("overwrote existing config")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("config changed")
	}
}

func TestDiffAndMouseSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "mouse = false\n[diff]\nrenderer = 'builtin'\nlayout = 'side-by-side'\ntheme = 'light'\nsyntax_theme = 'GitHub'\n[tools]\ndelta = '/optional/delta'\nrg = '/optional/rg'\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mouse || cfg.Diff.Renderer != "builtin" || cfg.Diff.Theme != "light" || cfg.Tools.RG != "/optional/rg" {
		t.Fatalf("settings lost: %+v", cfg)
	}
	if cfg.Tools.Chezmoi != "chezmoi" || !cfg.AutoFetch {
		t.Fatalf("legacy defaults lost: %+v", cfg)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.Diff.Renderer = "bad" }, func(c *Config) { c.Diff.Layout = "bad" }, func(c *Config) { c.Diff.Theme = "bad" }} {
		invalid := cfg
		mutate(&invalid)
		if Validate(invalid) == nil {
			t.Fatal("accepted invalid diff setting")
		}
	}
}
