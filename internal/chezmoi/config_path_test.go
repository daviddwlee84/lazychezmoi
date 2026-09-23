package chezmoi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func putConfigPath(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigPathFormatsSearchOrderAndExplicitOverride(t *testing.T) {
	for _, extension := range []string{"json", "jsonc", "toml", "yaml", "yml"} {
		t.Run(extension, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			configHome := filepath.Join(root, "config-home")
			system := filepath.Join(root, "system")
			env := map[string]string{"XDG_CONFIG_HOME": configHome, "XDG_CONFIG_DIRS": system}
			getenv := func(key string) string { return env[key] }
			first := filepath.Join(configHome, "chezmoi", "chezmoi."+extension)
			second := filepath.Join(system, "chezmoi", "chezmoi.toml")
			putConfigPath(t, second)
			got, err := locateChezmoiConfig("", home, getenv)
			if err != nil || !samePath(got, second) {
				t.Fatalf("system fallback %s: %v", got, err)
			}
			putConfigPath(t, first)
			got, err = locateChezmoiConfig("", home, getenv)
			if err != nil || !samePath(got, first) {
				t.Fatalf("home precedence %s: %v", got, err)
			}
			extra := "toml"
			if extension == extra {
				extra = "json"
			}
			putConfigPath(t, filepath.Join(configHome, "chezmoi", "chezmoi."+extra))
			if _, err := locateChezmoiConfig("", home, getenv); err == nil || !strings.Contains(err.Error(), "multiple") {
				t.Fatalf("multiple config files: %v", err)
			}
			got, err = locateChezmoiConfig("~/explicit.toml", home, getenv)
			if err != nil || !samePath(got, filepath.Join(home, "explicit.toml")) {
				t.Fatalf("explicit path must win even missing: %s %v", got, err)
			}
		})
	}
}

func TestConfigPathFallbackDirectoryNamesAndNativeExpansion(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	emptySystem := filepath.Join(root, "empty-system")
	env := map[string]string{"XDG_CONFIG_DIRS": emptySystem, "APPDATA": filepath.Join(root, "wrong-appdata")}
	getenv := func(key string) string { return env[key] }
	got, err := locateChezmoiConfig("", home, getenv)
	want := filepath.Join(home, ".config", "chezmoi", "chezmoi.toml")
	if err != nil || !samePath(got, want) {
		t.Fatalf("default (including Windows): %s %v", got, err)
	}
	// Native discovery matches directory-entry names before it validates file type.
	if err := os.MkdirAll(want, 0700); err != nil {
		t.Fatal(err)
	}
	got, err = locateChezmoiConfig("", home, getenv)
	if err != nil || !samePath(got, want) {
		t.Fatalf("directory name candidate: %s %v", got, err)
	}
	t.Chdir(root)
	env["XDG_CONFIG_HOME"] = "relative-config"
	got, err = locateChezmoiConfig("", home, getenv)
	if err != nil || !samePath(got, filepath.Join(root, "relative-config", "chezmoi", "chezmoi.toml")) {
		t.Fatalf("relative XDG: %s %v", got, err)
	}
	env["XDG_CONFIG_HOME"] = "~/tilde-config"
	got, err = locateChezmoiConfig("", home, getenv)
	if err != nil || !samePath(got, filepath.Join(home, "tilde-config", "chezmoi", "chezmoi.toml")) {
		t.Fatalf("tilde XDG: %s %v", got, err)
	}
	got, err = locateChezmoiConfig("~", home, getenv)
	if err != nil || !samePath(got, home) {
		t.Fatalf("exact tilde: %s %v", got, err)
	}
}

func isolateNativeHome(t *testing.T, home, system string) {
	t.Helper()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv("XDG_CONFIG_DIRS", system)
	// Native defaults must not inherit another chezmoi invocation's home context.
	for _, key := range []string{"CHEZMOI_CONFIG", "CHEZMOI_CONFIG_FILE", "CHEZMOI_SOURCE_DIR", "CHEZMOI_DEST_DIR", "CHEZMOI_WORKING_TREE"} {
		t.Setenv(key, "")
	}
}

func TestConfigLocatorMatchesNativeOnIsolatedHomes(t *testing.T) {
	for _, name := range []string{"default", "relative", "tilde", "system", "explicit", "yaml", "json", "jsonc", "yml"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			home := filepath.Join(f.root, "home")
			system := filepath.Join(f.root, "system")
			isolateNativeHome(t, home, system)
			t.Chdir(f.root)
			t.Setenv("XDG_CONFIG_HOME", "")
			opts := f.s.opts
			opts.ConfigFile = ""
			path := filepath.Join(home, ".config", "chezmoi", "chezmoi.toml")
			switch name {
			case "relative":
				t.Setenv("XDG_CONFIG_HOME", "relative")
				path = filepath.Join(f.root, "relative", "chezmoi", "chezmoi.toml")
			case "tilde":
				t.Setenv("XDG_CONFIG_HOME", "~/other")
				path = filepath.Join(home, "other", "chezmoi", "chezmoi.toml")
			case "system":
				path = filepath.Join(system, "chezmoi", "chezmoi.toml")
			case "explicit":
				opts.ConfigFile = "explicit.toml"
				path = filepath.Join(f.root, "explicit.toml")
			case "yaml", "json", "jsonc", "yml":
				path = strings.TrimSuffix(path, "toml") + name
			}
			putConfigPath(t, path)
			if name == "json" || name == "jsonc" {
				if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s := New(opts)
			resolved, err := s.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// Only this tiny isolated fixture invokes a literal template, as an
			// independent oracle for the native config filename expansion.
			native, err := s.read(context.Background(), "execute-template", "--init", "{{ .chezmoi.configFile }}")
			if err != nil {
				t.Fatal(err)
			}
			if !samePath(resolved.ConfigFile, strings.TrimSpace(string(native))) {
				t.Fatalf("locator %q differs from native %q", resolved.ConfigFile, native)
			}
		})
	}
}

func TestDefaultResolveDoesNotReadIgnoredTemplateData(t *testing.T) {
	f := newFixture(t)
	home := filepath.Join(f.root, "home")
	isolateNativeHome(t, home, filepath.Join(f.root, "system"))
	t.Setenv("XDG_CONFIG_HOME", "")
	putConfigPath(t, filepath.Join(home, ".config", "chezmoi", "chezmoi.toml"))
	f.write(t, "dot_one", "one\n")
	f.write(t, ".venv/site/deep/.chezmoidata.toml", "invalid = [ this data must not be parsed\n")
	f.write(t, ".venv/site/deep/.chezmoitemplates/invalid", "{{ invalid template syntax }")
	opts := f.s.opts
	opts.ConfigFile = ""
	s := New(opts)
	if _, err := s.Resolve(context.Background()); err != nil {
		t.Fatalf("cheap metadata traversed ignored tree: %v", err)
	}
	entries, err := s.Inventory(context.Background(), false)
	if err != nil || len(entries) != 1 || entries[0].Relative != ".one" {
		t.Fatalf("inventory traversed ignored tree: %+v %v", entries, err)
	}
}
