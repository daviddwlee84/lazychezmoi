package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/term"
)

// The compiled test binary is a portable fake lazychezmoi executable. It calls
// the real handoff API, so shell tests exercise descriptor/file inheritance too.
func TestMain(m *testing.M) {
	if os.Getenv("LAZYCHEZMOI_TEST_SHELL_CHILD") == "1" {
		os.Exit(shellChild())
	}
	os.Exit(m.Run())
}

func shellChild() int {
	args := os.Args[1:]
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "inspect":
		data := struct {
			Args      []string `json:"args"`
			Available bool     `json:"available"`
			Channel   string   `json:"channel"`
		}{args[1:], Available(), os.Getenv(fileEnv)}
		_ = json.NewEncoder(os.Stdout).Encode(data)
	case "stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "request", "request-fail", "tty-request":
		if args[0] == "tty-request" {
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				fmt.Fprintln(os.Stderr, "child did not inherit the terminal")
				return 4
			}
			fmt.Fprintln(os.Stdout, "CHILD_TTY")
		}
		if err := Request(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		fmt.Fprintln(os.Stdout, "child stdout")
		fmt.Fprintln(os.Stderr, "child stderr")
		if args[0] == "request-fail" {
			return 23
		}
	case "invalid":
		f, err := openChannel()
		if err != nil {
			return 3
		}
		_, _ = io.WriteString(f, args[1])
		_ = f.Close()
	case "fail":
		return 23
	default:
		return 2
	}
	return 0
}

func TestGenerate(t *testing.T) {
	for _, name := range []string{"bash", "zsh", "fish", "powershell", "pwsh"} {
		script, err := Generate(name, "/tool with space/it's-lazychezmoi")
		if err != nil || strings.Contains(script, "@@") || !strings.Contains(script, token) {
			t.Fatalf("%s: unresolved template or error: %v", name, err)
		}
	}
	for _, name := range []string{"", "sh", "cmd"} {
		if _, err := Generate(name, "lazychezmoi"); err == nil {
			t.Errorf("accepted unsupported shell %q", name)
		}
	}
	for _, path := range []string{"", "bad\npath", "bad\x00path"} {
		if _, err := Generate("bash", path); err == nil {
			t.Errorf("accepted invalid executable %q", path)
		}
	}
}

func TestChannelValidation(t *testing.T) {
	clearChannel(t)
	if Available() || Validate(true) == nil || Request() == nil {
		t.Fatal("missing integration must not permit reload")
	}
	channel := filepath.Join(t.TempDir(), "reload")
	if err := os.WriteFile(channel, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(kindEnv, "powershell")
	t.Setenv(fileEnv, channel)
	if !Available() || Validate(true) != nil {
		t.Fatal("valid private channel unavailable")
	}
	if Validate(false) == nil {
		t.Fatal("noninteractive reload allowed")
	}
	if err := Request(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(channel)
	if err != nil || string(got) != token {
		t.Fatalf("got %q: %v", got, err)
	}
	if Available() || Request() == nil {
		t.Fatal("reused or nonempty channel accepted")
	}
	t.Setenv(fileEnv, t.TempDir())
	if Available() {
		t.Fatal("directory accepted as channel")
	}
	t.Setenv(fileEnv, "relative")
	if Available() {
		t.Fatal("relative path accepted")
	}
}

func clearChannel(t *testing.T) {
	t.Helper()
	for _, key := range []string{kindEnv, fdEnv, fileEnv} {
		t.Setenv(key, "")
	}
}

func TestPOSIXWrappers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX integration is exercised on Linux and macOS")
	}
	for _, name := range []string{"bash", "zsh", "fish"} {
		t.Run(name, func(t *testing.T) {
			binary, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s unavailable", name)
			}
			fixture := newShellFixture(t, name)
			invoke := func(body, input string) (string, error) {
				t.Helper()
				script := ". " + shellQuote(fixture.wrapper) + "\n" + body
				flags := []string{"--noprofile", "--norc", "-c"}
				if name == "zsh" {
					flags = []string{"-f", "-c"}
				}
				if name == "fish" {
					flags = []string{"--no-config", "-c"}
				}
				cmd := exec.Command(binary, append(flags, script)...)
				cmd.Env = fixture.env
				cmd.Stdin = strings.NewReader(input)
				out, err := cmd.CombinedOutput()
				return string(out), err
			}

			out, err := invoke("lazychezmoi inspect 'with space' '' 'literal;$(no)'", "")
			if err != nil {
				t.Fatalf("inspect: %v: %s", err, out)
			}
			var got struct {
				Args      []string `json:"args"`
				Available bool     `json:"available"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil || !got.Available || fmt.Sprint(got.Args) != "[with space  literal;$(no)]" {
				t.Fatalf("argument/channel round trip: %+v; %v; output %q", got, err, out)
			}
			out, err = invoke("lazychezmoi stdin", "original stdin\n")
			if err != nil || out != "original stdin\n" {
				t.Fatalf("stdin not preserved: %q; %v", out, err)
			}
			for _, action := range []string{"fail", "request-fail"} {
				out, err := invoke("lazychezmoi "+action, "")
				assertExitCode(t, err, 23, out)
				if strings.Contains(out, "RELOADED") {
					t.Fatalf("failed operation reloaded: %s", out)
				}
			}
			for _, invalid := range []string{"reload-v1\n", "echo injected", "reload-v2"} {
				out, err := invoke("lazychezmoi invalid "+shellQuote(invalid), "")
				assertExitCode(t, err, 1, out)
				if !strings.Contains(out, "invalid shell reload response") {
					t.Fatalf("invalid token not diagnosed: %q", out)
				}
			}

			// The login profile exits immediately and reports its PID, proving
			// exec replaced the calling shell instead of adding another layer.
			body := `printf 'PARENT:%s\n' "$$"; lazychezmoi request`
			if name == "fish" {
				body = `printf 'PARENT:%s\n' $fish_pid; lazychezmoi request`
			}
			out, err = invoke(body, "")
			if err != nil {
				t.Fatalf("reload: %v: %s", err, out)
			}
			var parent, reloaded string
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "PARENT:") {
					parent = strings.TrimPrefix(line, "PARENT:")
				}
				if strings.HasPrefix(line, "RELOADED:") {
					reloaded = strings.TrimPrefix(line, "RELOADED:")
				}
			}
			if parent == "" || parent != reloaded || !strings.Contains(out, "child stdout") || !strings.Contains(out, "child stderr") {
				t.Fatalf("reload did not preserve parent PID/streams: %q", out)
			}
		})
	}
}

type shellFixture struct {
	wrapper string
	home    string
	env     []string
}

func newShellFixture(t *testing.T, name string) shellFixture {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The explicit executable includes whitespace and an apostrophe so quoting
	// is verified by real interpreters, not merely by string assertions.
	copyPath := filepath.Join(dir, "the tool's binary")
	if runtime.GOOS == "windows" {
		copyPath += ".exe"
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0700); err != nil {
		t.Fatal(err)
	}
	script, err := Generate(name, copyPath)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "wrapper")
	if name == "powershell" {
		wrapper += ".ps1"
	}
	if err := os.WriteFile(wrapper, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{".bash_profile", ".zprofile"} {
		if err := os.WriteFile(filepath.Join(dir, profile), []byte("printf 'RELOADED:%s\\n' \"$$\"\nexit 0\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fishDir := filepath.Join(dir, ".config", "fish")
	if err := os.MkdirAll(fishDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fishDir, "config.fish"), []byte("printf 'RELOADED:%s\\n' $fish_pid\nexit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := make([]string, 0, len(os.Environ())+8)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "LAZYCHEZMOI_RELOAD_") || key == "HOME" || key == "ZDOTDIR" || key == "XDG_CONFIG_HOME" || key == "BASH_ENV" || key == "ENV" {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "LAZYCHEZMOI_TEST_SHELL_CHILD=1", "HOME="+dir, "ZDOTDIR="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, ".config"))
	return shellFixture{wrapper: wrapper, home: dir, env: env}
}

func assertExitCode(t *testing.T, err error, code int, out string) {
	t.Helper()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != code {
		t.Fatalf("want exit %d, got %v: %s", code, err, out)
	}
}

func TestPOSIXPTYReload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PTY; PowerShell runs in the native Windows tests")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable for PTY verification")
	}
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			binary, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s unavailable", name)
			}
			fixture := newShellFixture(t, name)
			cmd := exec.Command(python, "-c", ptyTestDriver, binary, name, fixture.wrapper)
			cmd.Env = fixture.env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("PTY: %v: %s", err, out)
			}
			parent := regexp.MustCompile(`PARENT:(\d+)`).FindSubmatch(out)
			reloaded := regexp.MustCompile(`RELOADED:(\d+)`).FindSubmatch(out)
			if len(parent) != 2 || len(reloaded) != 2 || string(parent[1]) != string(reloaded[1]) || !strings.Contains(string(out), "CHILD_TTY") {
				t.Fatalf("PTY reload did not preserve terminal/parent process: %s", out)
			}
		})
	}
}

// Input is written through the PTY master; the child must observe real input
// and output TTYs and exec the login shell with the same PID. The disposable
// login profile reports that PID and exits, restoring ownership to the driver.
const ptyTestDriver = `import errno, os, pty, select, shlex, signal, sys, time
binary, kind, wrapper = sys.argv[1:]
pid, master = pty.fork()
if pid == 0:
    flags = ['--noprofile', '--norc', '-i'] if kind == 'bash' else ['-f', '-i']
    os.execv(binary, [binary] + flags)
command = '. ' + shlex.quote(wrapper) + '\n' + 'printf "PARENT:%s\\n" "$$"; lazychezmoi tty-request\n'
os.write(master, command.encode())
output = bytearray()
deadline = time.monotonic() + 15
finished = False
while time.monotonic() < deadline:
    ready, _, _ = select.select([master], [], [], 0.1)
    if ready:
        try:
            chunk = os.read(master, 65536)
        except OSError as error:
            if error.errno == errno.EIO:
                finished = True
                break
            raise
        if not chunk:
            finished = True
            break
        output.extend(chunk)
if not finished:
    os.kill(pid, signal.SIGKILL)
_, status = os.waitpid(pid, 0)
os.close(master)
sys.stdout.buffer.write(output)
if not finished:
    sys.stderr.write('PTY reload timed out\n')
    sys.exit(1)
sys.exit(os.waitstatus_to_exitcode(status))
`

func TestPowerShellWrapper(t *testing.T) {
	binary, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh unavailable; Windows CI must install PowerShell 7")
	}
	fixture := newShellFixture(t, "powershell")
	var profiles []string
	for i := 0; i < 4; i++ {
		path := filepath.Join(fixture.home, "profile-"+strconv.Itoa(i)+".ps1")
		body := fmt.Sprintf("$ProfileOrder += '%d'\n", i)
		if i == 3 {
			body += "function ReloadedFunction { 'function persisted' }\nSet-Alias -Name ReloadedAlias -Value ReloadedFunction\n$ReloadedVariable = 'variable persisted'\n"
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, path)
	}
	setup := ". " + powerShellQuote(fixture.wrapper) + "\n$ProfileOrder = ''\n$PROFILE = [PSCustomObject]@{\n"
	for i, property := range []string{"AllUsersAllHosts", "AllUsersCurrentHost", "CurrentUserAllHosts", "CurrentUserCurrentHost"} {
		setup += property + " = " + powerShellQuote(profiles[i]) + "\n"
	}
	setup += "}\n"
	invoke := func(body string) (string, error) {
		t.Helper()
		cmd := exec.Command(binary, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", setup+body)
		cmd.Env = fixture.env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("ordinary invocation has no capability", func(t *testing.T) {
		out, err := invoke("lazychezmoi inspect 'with space' '' 'literal;$(no)'\nexit $LASTEXITCODE")
		var got struct {
			Available bool     `json:"available"`
			Args      []string `json:"args"`
		}
		if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Available || len(got.Args) != 3 || got.Args[1] != "" {
			t.Fatalf("normal invocation: %v; %q", err, out)
		}
	})
	t.Run("dot sourced capability cleans private file", func(t *testing.T) {
		out, err := invoke("$result = . lazychezmoi inspect\n$result\n$info = $result | ConvertFrom-Json\nif (Test-Path -LiteralPath $info.channel) { exit 9 }; exit $LASTEXITCODE")
		if err != nil || !strings.Contains(out, `"available":true`) {
			t.Fatalf("channel cleanup: %v; %q", err, out)
		}
	})
	t.Run("reload in caller scope", func(t *testing.T) {
		out, err := invoke(". lazychezmoi request\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\nReloadedFunction\nReloadedAlias\n$ReloadedVariable\n$ProfileOrder\nif (Get-Variable '__lc_*' -ErrorAction SilentlyContinue) { exit 8 }\nexit 0")
		if err != nil || strings.Count(out, "function persisted") != 2 || !strings.Contains(out, "variable persisted") || !strings.Contains(out, "0123") {
			t.Fatalf("profile definitions did not persist: %v; %q", err, out)
		}
	})
	t.Run("failure never reloads", func(t *testing.T) {
		out, err := invoke("$PSNativeCommandUseErrorActionPreference = $true\n$ErrorActionPreference = 'Stop'\n. lazychezmoi request-fail\nif ($ProfileOrder -or -not $PSNativeCommandUseErrorActionPreference) { exit 8 }; exit $LASTEXITCODE")
		assertExitCode(t, err, 23, out)
	})
	t.Run("argument preference restored", func(t *testing.T) {
		out, err := invoke("$PSNativeCommandArgumentPassing = 'Legacy'\n. lazychezmoi inspect '' 'has\"quote'\nif ($PSNativeCommandArgumentPassing -ne 'Legacy') { exit 8 }; exit $LASTEXITCODE")
		var got struct {
			Args []string `json:"args"`
		}
		if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got.Args) != 2 || got.Args[0] != "" || got.Args[1] != `has"quote` {
			t.Fatalf("argument preference or arguments changed: %v; %q", err, out)
		}
	})
	t.Run("unknown response never evaluated", func(t *testing.T) {
		out, err := invoke(". lazychezmoi invalid 'Write-Output injected'\nif ($ProfileOrder) { exit 8 }; exit $LASTEXITCODE")
		assertExitCode(t, err, 1, out)
	})
	t.Run("profile error reports partial success", func(t *testing.T) {
		bad := filepath.Join(fixture.home, "bad-profile.ps1")
		if err := os.WriteFile(bad, []byte("throw 'profile broke'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := invoke("$PROFILE.CurrentUserCurrentHost = " + powerShellQuote(bad) + "\n. lazychezmoi request\nexit $LASTEXITCODE")
		assertExitCode(t, err, 1, out)
		if !strings.Contains(out, "operation succeeded, but profile reload failed") {
			t.Fatalf("missing partial-success diagnostic: %q", out)
		}
	})
}
