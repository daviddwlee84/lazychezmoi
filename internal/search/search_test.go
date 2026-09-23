package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("LAZYCHEZMOI_TEST_RG"); mode != "" {
		if mode == "sleep" {
			_ = os.WriteFile(os.Getenv("LAZYCHEZMOI_TEST_RG_MARKER"), []byte("started"), 0600)
			time.Sleep(10 * time.Second)
			os.Exit(0)
		}
		if mode == "partial" {
			path := map[string]string{"text": os.Getenv("LAZYCHEZMOI_TEST_RG_PATH")}
			encoder := json.NewEncoder(os.Stdout)
			_ = encoder.Encode(map[string]any{"type": "begin", "data": map[string]any{"path": path}})
			_ = encoder.Encode(map[string]any{"type": "match", "data": map[string]any{"path": path, "lines": map[string]string{"text": "needle\n"}, "line_number": 1, "submatches": []map[string]int{{"start": 0, "end": 6}}}})
			_ = encoder.Encode(map[string]any{"type": "end", "data": map[string]any{"path": path, "binary_offset": nil}})
			fmt.Fprintln(os.Stderr, "fixture read denied")
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

type fixture struct {
	root, source, destination, config string
	owner                             *chezmoi.Service
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	binary, err := exec.LookPath("chezmoi")
	if err != nil {
		t.Skip("chezmoi unavailable")
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg unavailable")
	}
	root := t.TempDir()
	f := fixture{root: root, source: filepath.Join(root, "source"), destination: filepath.Join(root, "destination"), config: filepath.Join(root, "chezmoi.toml")}
	for _, dir := range []string{f.source, f.destination} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.config, []byte("[data]\nfixture = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.owner = chezmoi.New(chezmoi.Options{Binary: binary, ConfigFile: f.config, SourceDir: f.source, DestinationDir: f.destination, WorkingTree: f.source, CacheDir: filepath.Join(root, "cache"), PersistentState: filepath.Join(root, "state")})
	return f
}
func write(t *testing.T, root, path, text string) string {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSourceSearchHiddenHelpersIgnoresBinaryAndNoRendering(t *testing.T) {
	f := newFixture(t)
	write(t, f.source, "dot_config/app.toml.tmpl", "{{ fail \"MUST_NOT_RENDER\" }}\nneedle\n")
	helper := write(t, f.source, ".chezmoitemplates/helper with space", "你好 needle 😀\n")
	write(t, f.source, ".gitignore", "ignored.txt\n")
	write(t, f.source, "ignored.txt", "needle\n")
	write(t, f.source, ".specstory/history.txt", "needle\n")
	write(t, f.source, ".git/not-a-secret", "needle\n")
	write(t, f.source, ".chezmoitemplates/binary", "needle\n\x00binary\n")
	outside := write(t, f.root, "outside", "needle\n")
	_ = os.Symlink(outside, filepath.Join(f.source, "escape-link"))
	s := New(Options{})
	result, err := s.Search(context.Background(), f.owner, Query{Scope: "source", Text: "needle"})
	if err != nil {
		t.Fatalf("%v %+v", err, result)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("unexpected searched paths: %+v", result)
	}
	for _, m := range result.Matches {
		if strings.Contains(m.Path, "app.toml") && m.Entry == nil {
			t.Fatal("managed template not mapped")
		}
		if m.Path == helper {
			if m.Entry != nil || m.Line != 1 || len(m.Spans) != 1 || m.Text[m.Spans[0].Start:m.Spans[0].End] != "needle" {
				t.Fatalf("helper mapping/span: %+v", m)
			}
			preview, err := s.Preview(context.Background(), m)
			if err != nil || preview != "你好 needle 😀\n" {
				t.Fatalf("preview %q %v", preview, err)
			}
			m.Path = outside
			if _, err := s.Preview(context.Background(), m); err == nil {
				t.Fatal("preview escaped search root")
			}
		}
	}
}

func TestCurrentAllowlistAndArgumentBatches(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 55; i++ {
		name := "file-" + strconv.Itoa(i) + strings.Repeat("x", 160)
		write(t, f.source, name, "source-only\n")
		write(t, f.destination, name, "current needle\n")
	}
	write(t, f.source, "missing", "source-only\n")
	write(t, f.destination, "unmanaged-secret", "current needle\n")
	write(t, f.source, "binary", "source-only\n")
	write(t, f.destination, "binary", "needle\n\x00binary")
	result, err := New(Options{}).Search(context.Background(), f.owner, Query{Scope: "current", Text: "needle"})
	if err != nil || len(result.Matches) != 55 || result.Skipped < 1 {
		t.Fatalf("allowlist/batches: %+v %v", result, err)
	}
	for _, m := range result.Matches {
		if m.Entry == nil || m.Path != m.Entry.Target || strings.Contains(m.Path, "unmanaged") {
			t.Fatalf("unmanaged target searched: %+v", m)
		}
	}
}

func TestSessionInventoryRefreshDoesNotCacheSearchContents(t *testing.T) {
	f := newFixture(t)
	write(t, f.source, "existing", "source first\n")
	write(t, f.destination, "existing", "current first\n")
	s := New(Options{})
	ctx := context.Background()
	assertMatches := func(scope, keyword string, want int) {
		t.Helper()
		result, err := s.Search(ctx, f.owner, Query{Scope: scope, Text: keyword})
		if err != nil || len(result.Matches) != want {
			t.Fatalf("scope %s, query %q: matches=%d, want=%d, err=%v", scope, keyword, len(result.Matches), want, err)
		}
	}
	assertMatches("current", "first", 1)
	write(t, f.source, "existing", "source second\n")
	write(t, f.destination, "existing", "current second\n")
	assertMatches("source", "second", 1)
	assertMatches("current", "second", 1)
	assertMatches("current", "first", 0)

	// A new managed path enters the Current allowlist after refresh; existing
	// file contents above are always read live, even with cached path metadata.
	write(t, f.source, "added", "source second\n")
	write(t, f.destination, "added", "current second\n")
	assertMatches("current", "second", 1)
	f.owner.Invalidate()
	assertMatches("current", "second", 2)
}

func TestLiteralSmartCaseRegexAndLimit(t *testing.T) {
	f := newFixture(t)
	write(t, f.source, ".chezmoitemplates/data", "a.b\naXb\nNEEDLE\nneedle\n")
	s := New(Options{})
	for _, test := range []struct {
		text  string
		regex bool
		want  int
	}{{"a.b", false, 1}, {"a.b", true, 2}, {"needle", false, 2}, {"NEEDLE", false, 1}, {"absent", false, 0}} {
		result, err := s.Search(context.Background(), f.owner, Query{Scope: "source", Text: test.text, Regex: test.regex})
		if err != nil || len(result.Matches) != test.want {
			t.Fatalf("%+v: %+v %v", test, result, err)
		}
	}
	result, err := s.Search(context.Background(), f.owner, Query{Scope: "source", Text: "[", Regex: true})
	if err == nil || len(result.Errors) == 0 {
		t.Fatalf("invalid regex must not be empty success: %+v %v", result, err)
	}
	write(t, f.source, ".chezmoitemplates/many", strings.Repeat("LIMIT_NEEDLE\n", 1002))
	result, err = s.Search(context.Background(), f.owner, Query{Scope: "source", Text: "LIMIT_NEEDLE"})
	if err != nil || !result.Truncated || len(result.Matches) != 1000 {
		t.Fatalf("cap: %d truncated=%v %v", len(result.Matches), result.Truncated, err)
	}
}

func TestMissingRGAndCancellation(t *testing.T) {
	s := New(Options{RG: "does-not-exist-fixture-rg"})
	if _, err := s.Search(context.Background(), nil, Query{Scope: "source", Text: "x"}); err == nil || !strings.Contains(err.Error(), "ripgrep") {
		t.Fatalf("missing rg: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, nil, Query{Scope: "source", Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestPartialFailureAndRunningCancellation(t *testing.T) {
	f := newFixture(t)
	path := write(t, f.source, ".chezmoitemplates/helper", "needle\n")
	exe, _ := os.Executable()
	s := New(Options{RG: exe})
	t.Setenv("LAZYCHEZMOI_TEST_RG", "partial")
	t.Setenv("LAZYCHEZMOI_TEST_RG_PATH", path)
	result, err := s.Search(context.Background(), f.owner, Query{Scope: "source", Text: "needle"})
	if err == nil || len(result.Matches) != 1 || !strings.Contains(err.Error(), "fixture read denied") || len(result.Errors) == 0 {
		t.Fatalf("partial failure was hidden: %+v %v", result, err)
	}
	t.Setenv("LAZYCHEZMOI_TEST_RG", "sleep")
	marker := filepath.Join(f.root, "started")
	t.Setenv("LAZYCHEZMOI_TEST_RG_MARKER", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = s.Search(ctx, f.owner, Query{Scope: "source", Text: "needle"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("running search not cancelled: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("fixture ripgrep never started; cancellation path was not exercised")
	}
}

func TestSearchHelperEditorUsesNativePreference(t *testing.T) {
	f := newFixture(t)
	helper := write(t, f.source, ".chezmoitemplates/helper with space", "text\n")
	managed := write(t, f.source, "dot_managed", "text\n")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := "[edit]\ncommand = " + strconv.Quote(exe) + "\nargs = ['--wait', 'argument with space']\n"
	if err := os.WriteFile(f.config, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cmd, err := f.owner.EditSearchFile(context.Background(), helper)
	physical, _ := filepath.EvalSymlinks(helper)
	if err != nil || len(cmd.Args) != 4 || cmd.Args[1] != "--wait" || cmd.Args[2] != "argument with space" || cmd.Args[3] != physical {
		t.Fatalf("editor: %v %v", cmd, err)
	}
	if _, err := f.owner.EditSearchFile(context.Background(), managed); err == nil {
		t.Fatal("managed source bypassed chezmoi edit")
	}
	outside := write(t, f.root, "outside", "text")
	if _, err := f.owner.EditSearchFile(context.Background(), outside); err == nil {
		t.Fatal("outside file accepted")
	}
	if err := os.Symlink(outside, filepath.Join(f.source, "helper-link")); err == nil {
		if _, err := f.owner.EditSearchFile(context.Background(), filepath.Join(f.source, "helper-link")); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}

func TestSearchHelperEditorFallbackPreservesQuotedArgs(t *testing.T) {
	f := newFixture(t)
	helper := write(t, f.source, ".chezmoitemplates/helper", "text")
	exe, _ := os.Executable()
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(f.root, "the editor's binary")
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(name, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config, []byte("[edit]\nargs = ['--native-wait']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", `"`+name+`" --wait "argument with space"`)
	t.Setenv("EDITOR", "must-not-run")
	cmd, err := f.owner.EditSearchFile(context.Background(), helper)
	if err != nil {
		t.Fatal(err)
	}
	physical, _ := filepath.EvalSymlinks(helper)
	if len(cmd.Args) != 5 || cmd.Args[0] != name || cmd.Args[1] != "--wait" || cmd.Args[2] != "argument with space" || cmd.Args[3] != "--native-wait" || cmd.Args[4] != physical {
		t.Fatalf("argv: %q", cmd.Args)
	}
}
