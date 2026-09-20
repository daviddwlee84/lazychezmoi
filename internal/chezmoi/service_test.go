package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	s                                            *Service
	root, source, destination, config, extension string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	binary, err := exec.LookPath("chezmoi")
	if err != nil {
		t.Skip("chezmoi is required for isolated integration tests")
	}
	root := t.TempDir()
	f := fixture{root: root, source: filepath.Join(root, "source"), destination: filepath.Join(root, "destination"), config: filepath.Join(root, "config", "chezmoi.toml"), extension: "sh"}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "empty-global"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, dir := range []string{f.source, f.destination, filepath.Dir(f.config)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := "[data]\ngreeting = \"hello\"\n[status]\nexclude = [\"scripts\"]\n[diff]\nexclude = [\"scripts\"]\n"
	if runtime.GOOS == "windows" {
		f.extension = "ps1"
		interpreter, err := exec.LookPath("pwsh")
		if err != nil {
			interpreter, err = exec.LookPath("powershell")
		}
		if err != nil {
			t.Skip("PowerShell required for Windows fixture scripts")
		}
		config += "[interpreters.ps1]\ncommand = " + strconv.Quote(interpreter) + "\nargs = [\"-NoLogo\", \"-NoProfile\", \"-NonInteractive\", \"-ExecutionPolicy\", \"Bypass\", \"-File\"]\n"
	}
	if err := os.WriteFile(f.config, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	f.s = New(Options{Binary: binary, ConfigFile: f.config, SourceDir: f.source, DestinationDir: f.destination, WorkingTree: f.source, CacheDir: filepath.Join(root, "cache"), PersistentState: filepath.Join(root, "state.boltdb")})
	return f
}

func (f fixture) write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(f.source, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runOperation(t *testing.T, s *Service, op Operation) {
	t.Helper()
	cmd, err := s.Command(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Args = append(cmd.Args, "--no-tty")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", cmd.Args, err, data)
	}
}

func requireEntry(t *testing.T, s *Service, target string) Entry {
	t.Helper()
	e, err := s.Locate(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f fixture) script(label string) string {
	if f.extension == "ps1" {
		return "[IO.File]::AppendAllText('{{ .chezmoi.destDir }}/events', \"" + label + "`n\")\n"
	}
	return "#!/bin/sh\nprintf '" + label + "\\n' >> '{{ .chezmoi.destDir }}/events'\n"
}

func TestFileWorkflowUsesIsolatedContext(t *testing.T) {
	f := newFixture(t)
	f.write(t, "dot_config/plain.txt", "original\n")
	f.write(t, "dot_config/app.toml.tmpl", "greeting = {{ .greeting | quote }}\n")
	f.write(t, "dot_config/create_seed.txt", "seed\n")
	f.write(t, "dot_config/檔案 space.txt", "unicode\n")
	c, err := f.s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(c.SourceDir, f.source) || !samePath(c.SourceStateDir, f.source) || !samePath(c.DestinationDir, f.destination) || !samePath(c.ConfigFile, f.config) {
		t.Fatalf("wrong context: %+v", c)
	}
	entries, err := f.s.Entries(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected four files: %+v", entries)
	}
	for _, e := range entries {
		if e.Pending != "A" {
			t.Fatalf("expected added: %+v", e)
		}
	}
	tmpl := requireEntry(t, f.s, ".config/app.toml")
	if !tmpl.Template || tmpl.Kind != "file" {
		t.Fatalf("wrong template metadata: %+v", tmpl)
	}
	for view, want := range map[string]string{"source": "{{ .greeting | quote }}", "rendered": "greeting = \"hello\"", "diff": "+greeting = \"hello\""} {
		got, err := f.s.Preview(context.Background(), tmpl, view)
		if err != nil || !strings.Contains(got, want) {
			t.Fatalf("%s preview: %q %v", view, got, err)
		}
	}
	runOperation(t, f.s, Operation{Kind: "apply", Targets: []string{tmpl.Source}, ExcludeScripts: true})
	if _, err := os.Stat(filepath.Join(f.destination, ".config/plain.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selected apply touched sibling")
	}
	got, err := f.s.Preview(context.Background(), tmpl, "current")
	if err != nil || got != "greeting = \"hello\"\n" {
		t.Fatalf("current %q %v", got, err)
	}
	runOperation(t, f.s, Operation{Kind: "apply", ExcludeScripts: true})
	plain := requireEntry(t, f.s, ".config/plain.txt")
	if err := os.WriteFile(plain.Target, []byte("edited live\n"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err = f.s.Entries(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID == plain.ID && (e.Drift != "M" || e.Pending != "M") {
			t.Fatalf("drift metadata: %+v", e)
		}
	}
	runOperation(t, f.s, Operation{Kind: "re-add", Targets: []string{plain.Target}})
	data, err := os.ReadFile(plain.Source)
	if err != nil || string(data) != "edited live\n" {
		t.Fatalf("re-add %q %v", data, err)
	}
	for _, target := range []string{tmpl.Target, ".config/seed.txt"} {
		if _, err := f.s.Command(context.Background(), Operation{Kind: "re-add", Targets: []string{target}}); err == nil {
			t.Fatalf("unsafe re-add accepted %s", target)
		}
	}
	seed := requireEntry(t, f.s, ".config/seed.txt")
	if err := os.WriteFile(seed.Target, []byte("user owned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.write(t, "dot_config/create_seed.txt", "new baseline\n")
	runOperation(t, f.s, Operation{Kind: "apply", Targets: []string{seed.Target}, ExcludeScripts: true})
	data, _ = os.ReadFile(seed.Target)
	if string(data) != "user owned\n" {
		t.Fatalf("seed overwrite: %s", data)
	}
}

func TestModifyPreviewAndApplyPreserveLiveContent(t *testing.T) {
	f := newFixture(t)
	name, script := "modify_overlay.txt", "#!/bin/sh\ncat\nprintf 'managed\\n'\n"
	if f.extension == "ps1" {
		name += ".ps1"
		script = "$body = [Console]::In.ReadToEnd()\n[Console]::Write($body + \"managed`n\")\n"
	}
	f.write(t, name, script)
	if err := os.WriteFile(filepath.Join(f.destination, "overlay.txt"), []byte("user\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := requireEntry(t, f.s, "overlay.txt")
	if e.Kind != "modify" {
		t.Fatalf("wrong kind %+v", e)
	}
	got, err := f.s.Preview(context.Background(), e, "rendered")
	if err != nil || got != "user\nmanaged\n" {
		t.Fatalf("modify preview %q %v", got, err)
	}
	if _, err := f.s.Command(context.Background(), Operation{Kind: "re-add", Targets: []string{e.Target}}); err == nil {
		t.Fatal("modify re-add accepted")
	}
	runOperation(t, f.s, Operation{Kind: "apply", Targets: []string{e.Target}, ExcludeScripts: true})
	got, err = f.s.Preview(context.Background(), e, "current")
	if err != nil || got != "user\nmanaged\n" {
		t.Fatalf("modify apply %q %v", got, err)
	}
}

func TestSourceRootMappingAndNativeCommandArgv(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".chezmoiroot", "home\n")
	path := f.write(t, "home/dot_config/app.toml.tmpl", "enabled = true\n")
	c, err := f.s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(c.SourceStateDir, filepath.Join(f.source, "home")) {
		t.Fatalf("wrong source-state root: %+v", c)
	}
	e := requireEntry(t, f.s, path)
	for _, target := range []string{path, "dot_config/app.toml.tmpl", ".config/app.toml", e.Target} {
		got := requireEntry(t, f.s, target)
		if got.ID != e.ID {
			t.Fatalf("source mapping %+v", got)
		}
	}
	cmd, err := f.s.Command(context.Background(), Operation{Kind: "edit", Targets: []string{e.Source}, Apply: true, ExcludeScripts: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, "|")
	if !strings.Contains(joined, "|edit|") || !strings.Contains(joined, "|--apply|") || cmd.Args[len(cmd.Args)-1] != e.Target {
		t.Fatalf("wrong editor argv: %q", cmd.Args)
	}
	init, err := f.s.Command(context.Background(), Operation{Kind: "init", Targets: []string{"someone/dotfiles"}, Prompt: true, Apply: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(init.Args, "|"), "|init|--apply=false|--prompt|someone/dotfiles") {
		t.Fatalf("init argv %q", init.Args)
	}
}

func TestScriptResetIsGranularAndVerified(t *testing.T) {
	f := newFixture(t)
	c, err := f.s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "2.69.4" {
		t.Skip("state adapter fixture requires chezmoi 2.69.4")
	}
	onceSource := f.write(t, ".chezmoiscripts/tasks/run_once_before_01_once."+f.extension+".tmpl", f.script("once"))
	changeSource := f.write(t, ".chezmoiscripts/tasks/run_onchange_after_02_change."+f.extension+".tmpl", f.script("change"))
	f.write(t, "run_after_99_sibling."+f.extension+".tmpl", f.script("sibling"))
	f.write(t, "dot_unrelated", "must remain absent\n")
	once := requireEntry(t, f.s, onceSource)
	change := requireEntry(t, f.s, changeSource)
	for _, e := range []Entry{once, change} {
		start := time.Now()
		runOperation(t, f.s, Operation{Kind: "script-apply", Targets: []string{e.Source}})
		result, err := f.s.ScriptResult(context.Background(), e, start)
		if err != nil || result != "ran" {
			t.Fatalf("result %s %v", result, err)
		}
		records, err := f.s.ScriptRecords(context.Background(), e)
		if err != nil || len(records) != 1 {
			t.Fatalf("records %+v %v", records, err)
		}
		start = time.Now()
		runOperation(t, f.s, Operation{Kind: "script-apply", Targets: []string{e.Source}})
		result, err = f.s.ScriptResult(context.Background(), e, start)
		if err != nil || result != "skipped" {
			t.Fatalf("skipped result %s %v", result, err)
		}
		bad := append([]ScriptRecord(nil), records...)
		bad[0].Key = "unreviewed"
		if err := f.s.ResetScript(context.Background(), e, bad); err == nil {
			t.Fatal("unreviewed key accepted")
		}
		if err := f.s.ResetScript(context.Background(), e, records); err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		runOperation(t, f.s, Operation{Kind: "script-apply", Targets: []string{e.Source}})
		result, err = f.s.ScriptResult(context.Background(), e, start)
		if err != nil || result != "ran" {
			t.Fatalf("rerun result %s %v", result, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(f.destination, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "once\nonce\nchange\nchange\n" {
		t.Fatalf("wrong scripts ran: %q", data)
	}
	if _, err := os.Stat(filepath.Join(f.destination, ".unrelated")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("script apply touched regular file")
	}
	entries, err := f.s.Entries(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("script exclusions hid inventory: %+v", entries)
	}
}

func TestFailedScriptDoesNotRecordSuccessfulRun(t *testing.T) {
	f := newFixture(t)
	body := "#!/bin/sh\nexit 7\n"
	if f.extension == "ps1" {
		body = "exit 7\n"
	}
	path := f.write(t, "run_once_fail."+f.extension, body)
	e := requireEntry(t, f.s, path)
	cmd, err := f.s.Command(context.Background(), Operation{Kind: "script-apply", Targets: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Fatal("failed script reported success")
	}
	c, _ := f.s.Resolve(context.Background())
	if c.Version != "2.69.4" {
		return
	}
	result, err := f.s.ScriptResult(context.Background(), e, start)
	if err != nil || result != "unverifiable" {
		t.Fatalf("failed run verification %s %v", result, err)
	}
	records, err := f.s.ScriptRecords(context.Background(), e)
	if err != nil || len(records) != 0 {
		t.Fatalf("failed run history %+v %v", records, err)
	}
}

func TestNoConstructorIOAndReloadEnvironmentScrubbed(t *testing.T) {
	s := New(Options{Binary: "nonexistent-chezmoi-for-constructor-test"})
	t.Setenv("LAZYCHEZMOI_RELOAD_FILE", "private-parent-path")
	t.Setenv("LAZYCHEZMOI_RELOAD_TOKEN", "private-parent-token")
	t.Setenv("LAZYCHEZMOI_KEEP", "yes")
	cmd, err := s.Command(context.Background(), Operation{Kind: "init"})
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range cmd.Env {
		if strings.HasPrefix(strings.ToUpper(env), "LAZYCHEZMOI_RELOAD_") {
			t.Fatalf("reload ownership leaked: %s", env)
		}
	}
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "LAZYCHEZMOI_KEEP=yes") {
		t.Fatal("unrelated environment lost")
	}
}

func TestUnsupportedAdapterDoesNotMutate(t *testing.T) {
	s := New(Options{})
	s.resolved = &Context{Version: "99.0.0"}
	_, err := s.ScriptRecords(context.Background(), Entry{Kind: "script-once"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported adapter: %v", err)
	}
}

func TestInvalidateResolvesChangedSourceRoot(t *testing.T) {
	f := newFixture(t)
	first, err := f.s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, ".chezmoiroot", "nested\n")
	f.write(t, "nested/dot_file", "test")
	f.s.Invalidate()
	second, err := f.s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if samePath(first.SourceStateDir, second.SourceStateDir) || !samePath(second.SourceStateDir, filepath.Join(f.source, "nested")) {
		t.Fatalf("stale context: %+v %+v", first, second)
	}
}

func TestResolveWaitCanBeCancelled(t *testing.T) {
	s := New(Options{})
	s.resolveMu <- struct{}{}
	defer func() { <-s.resolveMu }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Resolve(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting resolve: %v", err)
	}
}

func TestSSHRemoteClassification(t *testing.T) {
	for remote, want := range map[string]bool{"git@example.com:user/repo.git": true, "ssh://example.com/repo": true, "https://example.com/repo": false, "file:///tmp/repo": false, "/tmp/repo": false, `C:\repo`: false, "relative/repo": false} {
		if got := isSSHRemote(remote); got != want {
			t.Errorf("%s: %t want %t", remote, got, want)
		}
	}
}

func TestGitStatusAndFetchDoNotApply(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git required")
	}
	f := newFixture(t)
	gitRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
		cmd.Env = append(ChildEnv(), "GIT_CONFIG_GLOBAL="+filepath.Join(f.root, "empty-global"), "GIT_CONFIG_NOSYSTEM=1")
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, data)
		}
	}
	gitRun(f.source, "init", "--initial-branch=main")
	gitRun(f.source, "config", "user.email", "fixture@example.invalid")
	gitRun(f.source, "config", "user.name", "Fixture")
	gitRun(f.source, "config", "core.hooksPath", filepath.Join(f.root, "no-hooks"))
	f.write(t, "dot_file", "one\n")
	gitRun(f.source, "add", ".")
	gitRun(f.source, "commit", "-m", "fixture")
	remote := filepath.Join(f.root, "remote.git")
	if err := os.MkdirAll(remote, 0700); err != nil {
		t.Fatal(err)
	}
	gitRun(remote, "init", "--bare", "--initial-branch=main")
	gitRun(f.source, "remote", "add", "origin", remote)
	gitRun(f.source, "push", "-u", "origin", "main")
	status, err := f.s.GitStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" || status.Upstream != "origin/main" || status.Dirty || status.Ahead != 0 || status.Behind != 0 {
		t.Fatalf("status %+v", status)
	}
	f.write(t, "dot_file", "local\n")
	if err := f.s.Fetch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	status, err = f.s.GitStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty || status.FetchedAt.IsZero() {
		t.Fatalf("fetch status %+v", status)
	}
	data, _ := os.ReadFile(filepath.Join(f.source, "dot_file"))
	if string(data) != "local\n" {
		t.Fatal("fetch changed worktree")
	}
	if _, err := os.Stat(filepath.Join(f.destination, ".file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fetch applied dotfile")
	}
}

func TestCommandRejectsUnsupportedActions(t *testing.T) {
	s := New(Options{})
	for _, op := range []Operation{{Kind: "unknown"}, {Kind: "re-add"}, {Kind: "script-apply"}, {Kind: "init", Targets: []string{"--force"}}} {
		if _, err := s.Command(context.Background(), op); err == nil {
			t.Fatal(fmt.Sprintf("accepted %+v", op))
		}
	}
}

func TestUpdateAndSourceCommandContracts(t *testing.T) {
	s := New(Options{Binary: "test-chezmoi"})
	cmd, err := s.Command(context.Background(), Operation{Kind: "update", Apply: true, Init: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "update --apply=true --init=true") {
		t.Fatalf("update command %s", joined)
	}
	cmd, err = s.Command(context.Background(), Operation{Kind: "update", Apply: true, Init: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "--init=false") {
		t.Fatalf("init false missing: %v", cmd.Args)
	}
	cmd, err = s.Command(context.Background(), Operation{Kind: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Args[len(cmd.Args)-1] != "edit" {
		t.Fatalf("source should open editor: %v", cmd.Args)
	}
	cmd, err = s.Command(context.Background(), Operation{Kind: "source", Apply: true, ExcludeScripts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "edit --apply --exclude=scripts") {
		t.Fatalf("source edit apply flags lost: %v", cmd.Args)
	}
}

func TestBackgroundFetchRequiresUpstreamAndRespectsCustomSSH(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git required")
	}
	f := newFixture(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", f.source}, args...)...)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, data)
		}
	}
	run("init", "--initial-branch=main")
	if err := f.s.Fetch(context.Background(), true); err == nil || !strings.Contains(err.Error(), "no configured upstream") {
		t.Fatalf("missing upstream: %v", err)
	}
	run("remote", "add", "origin", "git@example.invalid:fixture/repo")
	run("config", "branch.main.remote", "origin")
	run("config", "branch.main.merge", "refs/heads/main")
	t.Setenv("GIT_SSH_COMMAND", "custom-ssh-command")
	if err := f.s.Fetch(context.Background(), true); err == nil || !strings.Contains(err.Error(), "custom SSH") {
		t.Fatalf("custom transport not rejected: %v", err)
	}
	cmd, err := f.s.Command(context.Background(), Operation{Kind: "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "GIT_SSH_COMMAND=custom-ssh-command") {
		t.Fatal("manual fetch changed SSH transport")
	}
	if cmd.Args[len(cmd.Args)-1] != "origin" {
		t.Fatalf("fetch did not select tracking remote: %v", cmd.Args)
	}
}

func TestExternalRefreshIsSeparateFromFilesAndScripts(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("external content\n")) }))
	defer server.Close()
	f.write(t, ".chezmoiexternal.toml", "[\"external.txt\"]\ntype = \"file\"\nurl = "+strconv.Quote(server.URL+"/asset")+"\nrefreshPeriod = \"168h\"\n")
	f.write(t, "dot_unrelated", "not applied\n")
	f.write(t, "run_sibling."+f.extension+".tmpl", f.script("sibling"))
	entries, err := f.s.Entries(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Relative != ".unrelated" {
		t.Fatalf("synthetic external source leaked into editable rows: %+v", entries)
	}
	runOperation(t, f.s, Operation{Kind: "externals"})
	data, err := os.ReadFile(filepath.Join(f.destination, "external.txt"))
	if err != nil || string(data) != "external content\n" {
		t.Fatalf("external content %q %v", data, err)
	}
	for _, name := range []string{".unrelated", "events"} {
		if _, err := os.Stat(filepath.Join(f.destination, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("external refresh touched %s", name)
		}
	}
}

// The test executable doubles as a portable, noninteractive configured editor.
func TestEditorHelper(t *testing.T) {
	if os.Getenv("LAZYCHEZMOI_TEST_EDITOR") != "1" {
		return
	}
	path := os.Args[len(os.Args)-1]
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, err = f.WriteString("\n# edited by fixture\n")
	_ = f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestSelectedScriptOpensConfiguredEditor(t *testing.T) {
	f := newFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	config = append(config, []byte("\n[edit]\ncommand = "+strconv.Quote(executable)+"\nargs = [\"-test.run=TestEditorHelper\", \"--\"]\nminDuration = \"0s\"\n")...)
	if err := os.WriteFile(f.config, config, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAZYCHEZMOI_TEST_EDITOR", "1")
	path := f.write(t, ".chezmoiscripts/tasks/run_once_edit."+f.extension+".tmpl", f.script("must-not-run"))
	e := requireEntry(t, f.s, path)
	runOperation(t, f.s, Operation{Kind: "edit", Targets: []string{e.Source}, ExcludeScripts: false})
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "# edited by fixture") {
		t.Fatalf("script editor did not update source: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(f.destination, "events")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("editing script executed it")
	}
}

func TestRecordedScriptResetSurvivesRenderError(t *testing.T) {
	f := newFixture(t)
	c, err := f.s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "2.69.4" {
		t.Skip("state adapter requires 2.69.4")
	}
	name := "run_once_render_error." + f.extension + ".tmpl"
	path := f.write(t, name, f.script("once"))
	e := requireEntry(t, f.s, path)
	runOperation(t, f.s, Operation{Kind: "script-apply", Targets: []string{path}})
	f.write(t, name, "{{ .missingFixtureValue }}")
	records, err := f.s.ScriptRecords(context.Background(), e)
	if err != nil || len(records) != 1 {
		t.Fatalf("record-backed recovery unavailable: %+v %v", records, err)
	}
	if err := f.s.ResetScript(context.Background(), e, records); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ScriptRecords(context.Background(), e); err == nil {
		t.Fatal("unidentifiable history plus render failure should be explicit")
	}
	entries, err := f.s.Entries(context.Background(), true)
	if err == nil || len(entries) != 1 || entries[0].Pending != "?" {
		t.Fatalf("render error discarded useful inventory: %+v %v", entries, err)
	}
}

func TestIdenticalOnceScriptsExposeSharedHash(t *testing.T) {
	f := newFixture(t)
	c, err := f.s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "2.69.4" {
		t.Skip("state adapter requires 2.69.4")
	}
	a := f.write(t, "run_once_a."+f.extension+".tmpl", f.script("shared"))
	b := f.write(t, "run_once_b."+f.extension+".tmpl", f.script("shared"))
	ea := requireEntry(t, f.s, a)
	eb := requireEntry(t, f.s, b)
	runOperation(t, f.s, Operation{Kind: "script-apply", Targets: []string{a}})
	records, err := f.s.ScriptRecords(context.Background(), eb)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Description, ea.Relative) || !strings.Contains(records[0].Description, "shared") {
		t.Fatalf("shared content record: %+v %v", records, err)
	}
	if err := f.s.ResetScript(context.Background(), eb, records); err != nil {
		t.Fatal(err)
	}
	runOperation(t, f.s, Operation{Kind: "script-apply", Targets: []string{b}})
	data, err := os.ReadFile(filepath.Join(f.destination, "events"))
	if err != nil || string(data) != "shared\nshared\n" {
		t.Fatalf("shared rerun: %q %v", data, err)
	}
}

func TestSymlinkMappingAndPreviews(t *testing.T) {
	f := newFixture(t)
	linked := filepath.Join(f.destination, "linked-target.txt")
	if err := os.WriteFile(linked, []byte("linked content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.destination, ".link")
	if err := os.Symlink(linked, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink creation unavailable without Developer Mode/privilege: %v", err)
		}
		t.Fatal(err)
	}
	add := f.s.command(context.Background(), "add", "--no-tty", "--", link)
	if data, err := add.CombinedOutput(); err != nil {
		t.Fatalf("native symlink add: %v %s", err, data)
	}
	e := requireEntry(t, f.s, link)
	if e.Kind != "symlink" || e.Template || e.Encrypted {
		t.Fatalf("native symlink attributes: %+v", e)
	}
	if got := requireEntry(t, f.s, e.Source); got.ID != e.ID {
		t.Fatalf("symlink source mapping: %+v", got)
	}
	for _, view := range []string{"source", "current", "rendered"} {
		text, err := f.s.Preview(context.Background(), e, view)
		if err != nil || !samePath(strings.TrimSpace(text), linked) {
			t.Fatalf("symlink %s preview: %q %v", view, text, err)
		}
	}
	if _, err := f.s.Command(context.Background(), Operation{Kind: "re-add", Targets: []string{link}}); err == nil {
		t.Fatal("plain-file re-add accepted a symlink")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	// Removing the live symlink is intentional drift. Authorize replacement
	// explicitly inside this disposable fixture rather than suppressing drift
	// protection in the service.
	apply, err := f.s.Command(context.Background(), Operation{Kind: "apply", Targets: []string{e.Source}, ExcludeScripts: true})
	if err != nil {
		t.Fatal(err)
	}
	apply.Args = append(apply.Args, "--force", "--no-tty")
	if data, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("replace fixture symlink: %v %s", err, data)
	}
	actual, err := os.Readlink(link)
	if err != nil || !samePath(actual, linked) {
		t.Fatalf("symlink apply: %q %v", actual, err)
	}
}

func TestEncryptedEditorDelegatesDecryptAndReencrypt(t *testing.T) {
	f := newFixture(t)
	identity := filepath.Join(f.root, "fixture-age-identity.txt")
	keygen := f.s.command(context.Background(), "age-keygen", "--use-builtin-age=true", "--output", identity)
	// Never include generated identity material in failure output.
	if err := keygen.Run(); err != nil {
		t.Fatalf("temporary age identity generation failed: %v", err)
	}
	recipientCmd := f.s.command(context.Background(), "age-keygen", "--convert", identity)
	recipient, err := recipientCmd.Output()
	if err != nil {
		t.Fatalf("temporary age recipient conversion failed: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	configured := "encryption = \"age\"\nuseBuiltinAge = true\n" + string(config) + "\n[age]\nidentity = " + strconv.Quote(identity) + "\nrecipient = " + strconv.Quote(strings.TrimSpace(string(recipient))) + "\n[edit]\ncommand = " + strconv.Quote(executable) + "\nargs = [\"-test.run=TestEditorHelper\", \"--\"]\nminDuration = \"0s\"\n"
	if err := os.WriteFile(f.config, []byte(configured), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAZYCHEZMOI_TEST_EDITOR", "1")
	target := filepath.Join(f.destination, ".encrypted-fixture")
	const plaintext = "harmless encrypted fixture content\n"
	if err := os.WriteFile(target, []byte(plaintext), 0600); err != nil {
		t.Fatal(err)
	}
	add := f.s.command(context.Background(), "add", "--no-tty", "--encrypt", "--", target)
	if err := add.Run(); err != nil {
		t.Fatalf("native encrypted add failed: %v", err)
	}
	e := requireEntry(t, f.s, target)
	if !e.Encrypted || e.Template || e.Kind != "file" {
		t.Fatalf("native encrypted attributes: %+v", e)
	}
	if !strings.HasPrefix(filepath.Base(e.Source), "encrypted_") {
		t.Fatalf("native encryption prefix was not preserved: %s", filepath.Base(e.Source))
	}
	before, err := os.ReadFile(e.Source)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), plaintext) {
		t.Fatal("plaintext was stored in the encrypted source")
	}
	if got, err := f.s.Preview(context.Background(), e, "rendered"); err != nil || got != plaintext {
		t.Fatalf("encrypted render mismatch: %v", err)
	}
	if _, err := f.s.Command(context.Background(), Operation{Kind: "re-add", Targets: []string{target}}); err == nil {
		t.Fatal("plain-file re-add accepted encrypted entry")
	}
	runOperation(t, f.s, Operation{Kind: "edit", Targets: []string{e.Source}, ExcludeScripts: true})
	after, err := os.ReadFile(e.Source)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) || strings.Contains(string(after), plaintext) || strings.Contains(string(after), "# edited by fixture") {
		t.Fatal("editor did not re-encrypt the modified source")
	}
	preview, err := f.s.Preview(context.Background(), e, "rendered")
	if err != nil || !strings.Contains(preview, "# edited by fixture") {
		t.Fatalf("encrypted editor change cannot be decrypted: %v", err)
	}
	live, err := os.ReadFile(target)
	if err != nil || string(live) != plaintext {
		t.Fatal("edit without apply changed the live target")
	}
	runOperation(t, f.s, Operation{Kind: "apply", Targets: []string{target}, ExcludeScripts: true})
	live, err = os.ReadFile(target)
	if err != nil || !strings.Contains(string(live), "# edited by fixture") {
		t.Fatalf("encrypted apply failed: %v", err)
	}
	for _, item := range []struct {
		name, flag, kind string
		template         bool
	}{
		{".encrypted-template", "--template", "file", true},
		{".encrypted-seed", "--create", "create", false},
	} {
		name := filepath.Join(f.destination, item.name)
		if err := os.WriteFile(name, []byte("fixture data\n"), 0600); err != nil {
			t.Fatal(err)
		}
		add := f.s.command(context.Background(), "add", "--no-tty", "--encrypt", item.flag, "--", name)
		if err := add.Run(); err != nil {
			t.Fatalf("native encrypted %s add failed: %v", item.flag, err)
		}
		entry := requireEntry(t, f.s, name)
		if !entry.Encrypted || entry.Template != item.template || entry.Kind != item.kind {
			t.Fatalf("native encrypted prefix/suffix parsing: %+v", entry)
		}
	}
}

func TestEncryptedTemplateSuffixMetadata(t *testing.T) {
	for _, source := range []string{"encrypted_private_dot_config.tmpl.age", "encrypted_private_dot_config.tmpl.asc", "create_encrypted_private_dot_config.tmpl.age", "modify_encrypted_private_config.tmpl.asc"} {
		_, template, encrypted := attributes(source)
		if !template || !encrypted {
			t.Errorf("encrypted template suffix not recognized: %s", source)
		}
	}
}
