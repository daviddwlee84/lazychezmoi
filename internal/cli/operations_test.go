package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The current test executable acts as a platform-native argv recorder for
// transparent commands. It never executes a shell or accesses real dotfiles.
func TestMain(m *testing.M) {
	if os.Getenv("LAZYCHEZMOI_CLI_TEST_HELPER") == "1" {
		data, _ := json.Marshal(os.Args[1:])
		if err := os.WriteFile(os.Getenv("LAZYCHEZMOI_CLI_TEST_ARGS"), data, 0600); err != nil {
			os.Exit(99)
		}
		code, _ := strconv.Atoi(os.Getenv("LAZYCHEZMOI_CLI_TEST_EXIT"))
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestNativeUpdateDefaultsAndExitStatus(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LAZYCHEZMOI_CLI_TEST_HELPER", "1")
	path := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("LAZYCHEZMOI_CLI_TEST_ARGS", path)
	for _, test := range []struct {
		flags []string
		init  string
	}{{nil, "--init=true"}, {[]string{"--init=false"}, "--init=false"}} {
		_, _, err := invoke(t, append([]string{"--chezmoi", os.Args[0], "update"}, test.flags...)...)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		if err := json.Unmarshal(body, &values); err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"update", "--apply=true", test.init, "--no-tty"} {
			if !slices.Contains(values, required) {
				t.Fatalf("missing %s in %q", required, values)
			}
		}
	}
	t.Setenv("LAZYCHEZMOI_CLI_TEST_EXIT", "17")
	_, _, err := invoke(t, "--chezmoi", os.Args[0], "update")
	if ExitCode(err) != 17 {
		t.Fatalf("child status lost: %v code %d", err, ExitCode(err))
	}
}

func TestInitTypedPromptArgumentsArePreserved(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LAZYCHEZMOI_CLI_TEST_HELPER", "1")
	path := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("LAZYCHEZMOI_CLI_TEST_ARGS", path)
	_, _, err := invoke(t, "--chezmoi", os.Args[0], "init", "--prompt", "--promptBool", "Enable tools=false", "--promptString", "Name=A person")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"--prompt", "Enable tools=false", "Name=A person", "--apply=false", "--no-tty"} {
		if !slices.Contains(values, required) {
			t.Fatalf("missing %q in %q", required, values)
		}
	}
}

func TestCLIIsolatedFileWorkflow(t *testing.T) {
	native, err := exec.LookPath("chezmoi")
	if err != nil {
		t.Skip("chezmoi not installed")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	for _, dir := range []string{source, destination} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "dot_demo.toml.tmpl"), []byte("value = {{ add 1 1 }}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeConfig := filepath.Join(root, "chezmoi.toml")
	if err := os.WriteFile(nativeConfig, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ownConfig := filepath.Join(root, "lazychezmoi.toml")
	if err := os.WriteFile(ownConfig, nil, 0600); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--config", ownConfig, "--chezmoi", native, "--source", source, "--destination", destination, "--chezmoi-config", nativeConfig, "--chezmoi-cache", filepath.Join(root, "cache"), "--persistent-state", filepath.Join(root, "state.db")}
	call := func(arguments ...string) (string, error) {
		out, _, err := invoke(t, append(slices.Clone(flags), arguments...)...)
		return out, err
	}
	out, err := call("files", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Target   string `json:"target"`
		Template bool   `json:"template"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Template || filepath.Clean(filepath.FromSlash(entries[0].Target)) != filepath.Join(destination, ".demo.toml") {
		t.Fatalf("bad files JSON: %s", out)
	}
	out, err = call("preview", ".demo.toml", "--view", "rendered")
	if err != nil || out != "value = 2\n" {
		t.Fatalf("preview %q %v", out, err)
	}
	if _, err = call("apply", ".demo.toml"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, ".demo.toml"))
	if err != nil || string(data) != "value = 2\n" {
		t.Fatalf("apply %q %v", data, err)
	}
	_, err = call("re-add", ".demo.toml")
	if err == nil || !strings.Contains(err.Error(), "plain managed file") {
		t.Fatalf("template re-add was not refused: %v", err)
	}
}
