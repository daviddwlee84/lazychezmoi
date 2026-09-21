package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type inspectionFixture struct {
	flags           []string
	source, current string
}

func newInspectionFixture(t *testing.T) inspectionFixture {
	t.Helper()
	binary, err := exec.LookPath("chezmoi")
	if err != nil {
		t.Skip("chezmoi not installed")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	current := filepath.Join(root, "destination")
	configuration := filepath.Join(root, "config")
	for _, dir := range []string{source, current, configuration} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	native := filepath.Join(configuration, "chezmoi.toml")
	own := filepath.Join(configuration, "lazychezmoi.toml")
	for _, file := range []string{native, own} {
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "empty-gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return inspectionFixture{source: source, current: current, flags: []string{"--config", own, "--chezmoi", binary, "--source", source, "--destination", current, "--chezmoi-config", native, "--chezmoi-cache", filepath.Join(root, "cache"), "--persistent-state", filepath.Join(root, "state.db")}}
}

func (f inspectionFixture) call(t *testing.T, arguments ...string) (string, string, error) {
	t.Helper()
	return invoke(t, append(slices.Clone(f.flags), arguments...)...)
}
func writeInspection(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHunkCLICopiesOnlyInspectedRangeAndRejectsStaleID(t *testing.T) {
	f := newInspectionFixture(t)
	unchanged := "same1\nsame2\nsame3\nsame4\nsame5\nsame6\nsame7\nsame8\n"
	writeInspection(t, filepath.Join(f.source, "dot_demo"), "source first\n"+unchanged+"source last\n")
	writeInspection(t, filepath.Join(f.current, ".demo"), "current first\n"+unchanged+"current last\n")
	out, _, err := f.call(t, "hunks", ".demo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ID    string `json:"id"`
		Hunks []struct {
			ID string `json:"id"`
		} `json:"hunks"`
	}
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ID == "" || len(snapshot.Hunks) != 2 {
		t.Fatalf("unexpected snapshot: %s", out)
	}
	_, _, err = f.call(t, "copy-hunk", ".demo", "--id", snapshot.Hunks[0].ID, "--direction", "source-to-current", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.current, ".demo"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "source first\n"+unchanged+"current last\n" {
		t.Fatalf("copy changed wrong range: %q", got)
	}
	_, _, err = f.call(t, "copy-hunk", ".demo", "--id", snapshot.Hunks[1].ID, "--direction", "source-to-current", "--yes")
	if err == nil {
		t.Fatal("stale snapshot ID was accepted")
	}
	after, _ := os.ReadFile(filepath.Join(f.current, ".demo"))
	if string(after) != string(got) {
		t.Fatal("stale copy changed file")
	}
}

func TestSearchCLIScopesAndKeepsJSONClean(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	f := newInspectionFixture(t)
	writeInspection(t, filepath.Join(f.source, "dot_demo"), "source needle\n")
	writeInspection(t, filepath.Join(f.current, ".demo"), "current needle\n")
	writeInspection(t, filepath.Join(f.current, "unmanaged.txt"), "current needle\n")
	writeInspection(t, filepath.Join(f.source, ".chezmoitemplates", "helper.tmpl"), "helper needle\n")
	writeInspection(t, filepath.Join(f.source, ".specstory", "history.md"), "transcript needle\n")
	for _, scope := range []string{"source", "current"} {
		out, _, err := f.call(t, "search", "needle", "--scope", scope, "--json")
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Matches []struct {
				Path  string `json:"path"`
				Scope string `json:"scope"`
				Line  int    `json:"line"`
				Text  string `json:"text"`
			} `json:"matches"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("invalid JSON %q: %v", out, err)
		}
		want := 2
		if scope == "current" {
			want = 1
		}
		if len(result.Matches) != want {
			t.Fatalf("%s search wrong coverage: %s", scope, out)
		}
		for _, match := range result.Matches {
			if match.Scope != scope || match.Line != 1 {
				t.Fatalf("bad match: %+v", match)
			}
		}
	}
}

func TestDiffPreviewRawCompatibilityAndRendererFallback(t *testing.T) {
	f := newInspectionFixture(t)
	writeInspection(t, filepath.Join(f.source, "dot_demo"), "new\n")
	writeInspection(t, filepath.Join(f.current, ".demo"), "old\n")
	raw, _, err := f.call(t, "preview", ".demo", "--view=diff")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "-old") || !strings.Contains(raw, "+new") || strings.Contains(raw, "\x1b") {
		t.Fatalf("unexpected raw diff: %q", raw)
	}
	formatted, _, err := f.call(t, "--delta=missing-delta-for-test", "preview", ".demo", "--view=diff", "--renderer=delta", "--width=80", "--color=always")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted, "old") || !strings.Contains(formatted, "new") {
		t.Fatalf("fallback lost content: %q", formatted)
	}
}
