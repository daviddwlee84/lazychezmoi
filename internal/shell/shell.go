// Package shell supplies the parent-shell integration for explicit reloads.
// Ordinary command output is never interpreted as shell code.
package shell

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	kindEnv = "LAZYCHEZMOI_RELOAD_KIND"
	fdEnv   = "LAZYCHEZMOI_RELOAD_FD"
	fileEnv = "LAZYCHEZMOI_RELOAD_FILE"
	token   = "reload-v1"
)

// Generate prints a wrapper around an explicit executable. PowerShell callers
// dot-source the wrapper invocation when they want profiles to retain their
// definitions in the calling scope: . lazychezmoi update --reload.
func Generate(shellName, executable string) (string, error) {
	if executable == "" || strings.ContainsAny(executable, "\x00\r\n") {
		return "", errors.New("shell integration needs a nonempty executable path without line breaks")
	}
	var script string
	switch shellName {
	case "bash":
		script = strings.ReplaceAll(posixInit, "@@KIND@@", "bash")
		script = strings.ReplaceAll(script, "@@EXEC@@", `exec "$BASH" -l`)
	case "zsh":
		script = strings.ReplaceAll(posixInit, "@@KIND@@", "zsh")
		script = strings.ReplaceAll(script, "@@EXEC@@", `exec "${commands[zsh]:-zsh}" -l`)
	case "fish":
		script = fishInit
	case "powershell", "pwsh":
		return strings.ReplaceAll(powershellInit, "@@EXE@@", powerShellQuote(executable)), nil
	default:
		return "", fmt.Errorf("unsupported shell %q: want bash, zsh, fish or powershell", shellName)
	}
	return strings.ReplaceAll(script, "@@EXE@@", shellQuote(executable)), nil
}

func shellQuote(s string) string      { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func powerShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Available reports whether this invocation has a usable parent reload channel.
// It does not check terminal intent; callers must use Validate before mutation.
func Available() bool {
	f, err := openChannel()
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// Validate rejects an impossible reload before an update or apply can start.
func Validate(interactive bool) error {
	if !interactive {
		return errors.New("shell reload requires an interactive terminal and cannot be combined with JSON output")
	}
	if !Available() {
		return errors.New("shell reload needs shell integration: load `lazychezmoi shell-init <shell>`; in PowerShell invoke `. lazychezmoi update --reload` or `. lazychezmoi`")
	}
	return nil
}

// Request asks the parent wrapper to reload after this process exits with zero.
// The caller must finish terminal cleanup and propagate any operation failure.
func Request() error {
	f, err := openChannel()
	if err != nil {
		return fmt.Errorf("request shell reload: %w", err)
	}
	defer f.Close()
	n, err := io.WriteString(f, token)
	if err == nil && n != len(token) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("request shell reload: %w", err)
	}
	return nil
}

func openChannel() (*os.File, error) {
	switch os.Getenv(kindEnv) {
	case "bash", "zsh", "fish":
		if os.Getenv(fileEnv) != "" {
			return nil, errors.New("ambiguous reload channel")
		}
		fd, err := strconv.Atoi(os.Getenv(fdEnv))
		if err != nil || fd < 3 || fd > 255 {
			return nil, errors.New("invalid reload descriptor")
		}
		return duplicatePipe(fd)
	case "powershell":
		if os.Getenv(fdEnv) != "" {
			return nil, errors.New("ambiguous reload channel")
		}
		name := os.Getenv(fileEnv)
		if !filepath.IsAbs(name) {
			return nil, errors.New("reload channel path must be absolute")
		}
		before, err := os.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !before.Mode().IsRegular() || before.Size() != 0 {
			return nil, errors.New("reload channel must be an empty regular file")
		}
		f, err := os.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		after, err := f.Stat()
		if err != nil || !os.SameFile(before, after) || after.Size() != 0 {
			_ = f.Close()
			return nil, errors.New("reload channel changed while opening")
		}
		return f, nil
	default:
		return nil, errors.New("no parent reload channel")
	}
}

// An anonymous pipe captures only FD 3, while FD 4 keeps the child's stdout
// attached to the original terminal. Shell redirections close both descriptors
// on every return or interruption, without files or caller trap replacement.
// A suffix preserves trailing newlines so only the exact token is accepted.
const posixInit = `# lazychezmoi shell integration: eval "$(lazychezmoi shell-init @@KIND@@)"
function lazychezmoi {
  local __lc_reply __lc_status=0
  __lc_reply="$(
    __lc_child_status=0
    LAZYCHEZMOI_RELOAD_KIND=@@KIND@@ LAZYCHEZMOI_RELOAD_FD=3 LAZYCHEZMOI_RELOAD_FILE= command @@EXE@@ "$@" 3>&1 1>&4 || __lc_child_status=$?
    printf '.'
    exit "$__lc_child_status"
  )" || __lc_status=$?
  if [ "$__lc_status" -ne 0 ]; then
    return "$__lc_status"
  fi
  case "$__lc_reply" in
    '.') return 0 ;;
    'reload-v1.') @@EXEC@@ ;;
    *) printf 'lazychezmoi: invalid shell reload response; shell unchanged\n' >&2; return 1 ;;
  esac
} 4>&1
`

const fishInit = `# lazychezmoi shell integration: lazychezmoi shell-init fish | source
function __lazychezmoi_invoke
    set -lx LAZYCHEZMOI_RELOAD_KIND fish
    set -lx LAZYCHEZMOI_RELOAD_FD 3
    set -lx LAZYCHEZMOI_RELOAD_FILE ''
    command @@EXE@@ $argv 3>&1 1>&4
    set -l __lc_status $status
    printf '.'
    return $__lc_status
end
function lazychezmoi
    set -l __lc_reply
    set -l __lc_status
    begin
        set __lc_reply (__lazychezmoi_invoke $argv)
        set __lc_status $status
    end 4>&1
    if test $__lc_status -ne 0
        return $__lc_status
    end
    if test (count $__lc_reply) -ne 1
        printf 'lazychezmoi: invalid shell reload response; shell unchanged\n' >&2
        return 1
    end
    switch "$__lc_reply"
        case '.'
            return 0
        case 'reload-v1.'
            set -l __lc_fish (status fish-path)
            exec "$__lc_fish" -l
        case '*'
            printf 'lazychezmoi: invalid shell reload response; shell unchanged\n' >&2
            return 1
    end
end
`

const powershellInit = `# Load: Invoke-Expression (& lazychezmoi shell-init powershell | Out-String)
# Reload in the caller's scope: . lazychezmoi update --reload (or . lazychezmoi)
# Profile reload is additive; it does not restart PowerShell.
function global:lazychezmoi {
    if ($PSVersionTable.PSVersion -lt [Version]'7.3') {
        Write-Error 'lazychezmoi shell integration requires PowerShell 7.3 or later for native argument preservation' -ErrorAction Continue
        $global:LASTEXITCODE = 2
        return
    }
    $__lc_saved = @{}
    $__lc_nativeArguments = $PSNativeCommandArgumentPassing
    $__lc_nativeErrors = $PSNativeCommandUseErrorActionPreference
    $__lc_channel = $null
    $__lc_reload = $false
    $__lc_status = 1
    foreach ($__lc_name in @('LAZYCHEZMOI_RELOAD_KIND', 'LAZYCHEZMOI_RELOAD_FD', 'LAZYCHEZMOI_RELOAD_FILE')) {
        $__lc_saved[$__lc_name] = [Environment]::GetEnvironmentVariable($__lc_name, 'Process')
        [Environment]::SetEnvironmentVariable($__lc_name, $null, 'Process')
    }
    try {
        if ($MyInvocation.InvocationName -eq '.') {
            $__lc_channel = [System.IO.Path]::GetTempFileName()
            $env:LAZYCHEZMOI_RELOAD_KIND = 'powershell'
            $env:LAZYCHEZMOI_RELOAD_FILE = $__lc_channel
        }
        $PSNativeCommandArgumentPassing = 'Standard'
        $PSNativeCommandUseErrorActionPreference = $false
        & @@EXE@@ @args
        $__lc_status = $LASTEXITCODE
        if ($__lc_status -eq 0 -and $__lc_channel) {
            $__lc_reply = [System.IO.File]::ReadAllText($__lc_channel)
            if ($__lc_reply -ceq 'reload-v1') {
                $__lc_reload = $true
            } elseif ($__lc_reply.Length -ne 0) {
                Write-Error 'lazychezmoi: invalid shell reload response; profiles unchanged' -ErrorAction Continue
                $__lc_status = 1
            }
        }
    } catch {
        Write-Error "lazychezmoi: $_" -ErrorAction Continue
        $__lc_status = 1
    } finally {
        $PSNativeCommandArgumentPassing = $__lc_nativeArguments
        $PSNativeCommandUseErrorActionPreference = $__lc_nativeErrors
        if ($__lc_channel) {
            Remove-Item -LiteralPath $__lc_channel -Force -ErrorAction SilentlyContinue
        }
        foreach ($__lc_name in $__lc_saved.Keys) {
            [Environment]::SetEnvironmentVariable($__lc_name, $__lc_saved[$__lc_name], 'Process')
        }
    }
    if ($__lc_status -eq 0 -and $__lc_reload) {
        try {
            $__lc_profiles = @($PROFILE.AllUsersAllHosts, $PROFILE.AllUsersCurrentHost, $PROFILE.CurrentUserAllHosts, $PROFILE.CurrentUserCurrentHost)
            foreach ($__lc_profile in $__lc_profiles) {
                if ($__lc_profile -and (Test-Path -LiteralPath $__lc_profile -PathType Leaf)) {
                    . $__lc_profile
                }
            }
        } catch {
            Write-Error "lazychezmoi: operation succeeded, but profile reload failed: $_" -ErrorAction Continue
            $__lc_status = 1
        }
    }
    $global:LASTEXITCODE = $__lc_status
    Remove-Variable __lc_saved, __lc_channel, __lc_reload, __lc_status, __lc_name, __lc_reply, __lc_profiles, __lc_profile, __lc_nativeArguments, __lc_nativeErrors -ErrorAction SilentlyContinue
}
`
