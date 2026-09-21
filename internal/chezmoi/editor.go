package chezmoi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// EditSearchFile opens an unmanaged repository helper using the native editor
// preference. Managed entries must use chezmoi edit, retaining its semantics.
// Building this command does not start the editor; the caller owns TTY handoff.
func (s *Service) EditSearchFile(ctx context.Context, absolutePath string) (*exec.Cmd, error) {
	if !filepath.IsAbs(absolutePath) {
		return nil, errors.New("search editor requires an absolute path")
	}
	cx, err := s.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(cx.SourceDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("search editor requires an existing regular file, not a symlink")
	}
	physical, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, physical)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("search editor file is outside the source repository")
	}
	for _, scripts := range []bool{false, true} {
		entries, err := s.Inventory(ctx, scripts)
		if err != nil {
			return nil, fmt.Errorf("verify unmanaged editor target: %w", err)
		}
		for _, entry := range entries {
			known, knownErr := filepath.EvalSymlinks(entry.Source)
			if samePath(entry.Source, absolutePath) || samePath(entry.Source, physical) || knownErr == nil && samePath(known, physical) {
				return nil, errors.New("managed search results must use chezmoi edit")
			}
		}
	}
	data, err := s.read(ctx, "dump-config", "--format=json")
	if err != nil {
		return nil, err
	}
	var config struct {
		Edit struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"edit"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, errors.New("chezmoi returned invalid editor configuration")
	}
	command, args := config.Edit.Command, append([]string{}, config.Edit.Args...)
	if command == "" {
		value := os.Getenv("VISUAL")
		if strings.TrimSpace(value) == "" {
			value = os.Getenv("EDITOR")
		}
		if strings.TrimSpace(value) == "" {
			value = "vi"
			if runtime.GOOS == "windows" {
				value = "notepad.exe"
			}
		}
		// Try the whole value first, supporting an executable path containing
		// spaces (notably native Windows), then tokenize argument-bearing values.
		if path, lookupErr := exec.LookPath(value); lookupErr == nil {
			command = path
		} else {
			fields, parseErr := editorFields(value)
			if parseErr != nil || len(fields) == 0 {
				return nil, errors.New("invalid editor command; use a quoted executable plus arguments or native edit.command/edit.args")
			}
			command, args = fields[0], append(fields[1:], args...)
		}
	}
	if command == "" {
		return nil, errors.New("editor executable is empty")
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return nil, fmt.Errorf("configured editor is unavailable: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, append(args, physical)...)
	cmd.Dir, cmd.Env = root, ChildEnv()
	cmd.WaitDelay = time.Second
	return cmd, nil
}

// editorFields handles quoting for editor argv, without expansion, globbing,
// command substitution, pipelines or a shell interpreter. Backslashes in native
// Windows paths survive; quotes and escaped spaces remain usable on Unix.
func editorFields(value string) ([]string, error) {
	var result []string
	var field strings.Builder
	var quote rune
	started := false
	runes := []rune(value)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0 || r == '\n' || r == '\r' {
			return nil, errors.New("editor command contains a control character")
		}
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				field.WriteRune(r)
			}
			continue
		}
		if r == '\\' && i+1 < len(runes) {
			next := runes[i+1]
			if next == '\\' || next == '"' || quote == 0 && (next == '\'' || unicode.IsSpace(next)) {
				field.WriteRune(next)
				i++
				started = true
				continue
			}
		}
		if quote == '"' {
			if r == '"' {
				quote = 0
			} else {
				field.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				result = append(result, field.String())
				field.Reset()
				started = false
			}
			continue
		}
		field.WriteRune(r)
		started = true
	}
	if quote != 0 {
		return nil, errors.New("unterminated editor quote")
	}
	if started {
		result = append(result, field.String())
	}
	return result, nil
}
