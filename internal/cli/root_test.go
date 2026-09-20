package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func invoke(t *testing.T, arguments ...string) (string, string, error) {
	t.Helper()
	root := New("test-version")
	var out, errOut bytes.Buffer
	root.SetArgs(arguments)
	root.SetIn(strings.NewReader(""))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestEntryIntent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, test := range []struct {
		args []string
		want string
		code int
	}{
		{nil, "Usage:", 0},
		{[]string{"tui"}, "", 2},
		{[]string{"preview"}, "", 2},
		{[]string{"preview", "file", "--view", "typo"}, "", 2},
		{[]string{"--bad-flag"}, "", 2},
		{[]string{"--color=typo"}, "", 2},
		{[]string{"scripts"}, "Available Commands:", 0},
		{[]string{"config"}, "Available Commands:", 0},
		{[]string{"version"}, "test-version", 0},
	} {
		out, _, err := invoke(t, test.args...)
		if ExitCode(err) != test.code || !strings.Contains(out, test.want) {
			t.Fatalf("%v: out=%q err=%v code=%d", test.args, out, err, ExitCode(err))
		}
	}
}

func TestHelpAndCompletionStayOfflineWithBrokenConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.toml")
	if err := os.WriteFile(path, []byte("not [toml"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range [][]string{{"--help"}, {"version"}, {"completion", "zsh"}, {"__complete", "preview", "--view", "r"}, {"config", "path"}} {
		out, _, err := invoke(t, append([]string{"--config", path, "--chezmoi", "does-not-exist"}, suffix...)...)
		if err != nil || out == "" {
			t.Fatalf("%v: out=%q err=%v", suffix, out, err)
		}
	}
}

func TestSettingsFlagsOverrideWithoutLosingFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("auto_fetch = true\ncolor = 'never'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, _, err := invoke(t, "--config", path, "--auto-fetch=false", "config", "show", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		AutoFetch bool   `json:"auto_fetch"`
		Color     string `json:"color"`
	}
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatal(err)
	}
	if value.AutoFetch || value.Color != "never" {
		t.Fatalf("%s", out)
	}
	_, _, err = invoke(t, "--config", path, "--color=invalid", "config", "show")
	if ExitCode(err) != 2 {
		t.Fatalf("invalid color: %v", err)
	}
}

func TestReloadRejectedBeforeBackend(t *testing.T) {
	_, _, err := invoke(t, "--chezmoi", "must-never-execute", "update", "--reload")
	if ExitCode(err) != 2 || strings.Contains(err.Error(), "executable file") {
		t.Fatalf("unexpected reload validation: %v", err)
	}
}

func TestVersionPrecedence(t *testing.T) {
	for _, v := range []struct{ injected, module, want string }{{"v1", "v2", "v1"}, {"dev", "v2", "v2"}, {"", "(devel)", "dev"}, {"dev", "v0.0.0-commit", "v0.0.0-commit"}} {
		if got := buildVersion(v.injected, v.module); got != v.want {
			t.Fatalf("%+v: %s", v, got)
		}
	}
}
