// Package search performs explicit textual searches without rendering templates
// or retaining file contents outside the current in-memory result.
package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

type Options struct{ RG string }
type Service struct{ opts Options }

func New(opts Options) *Service {
	if opts.RG == "" {
		opts.RG = "rg"
	}
	return &Service{opts: opts}
}

type Query struct {
	Scope, Text string
	Regex       bool
}
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type Match struct {
	Path     string         `json:"path"`
	Relative string         `json:"relative"`
	Scope    string         `json:"scope"`
	Line     int            `json:"line"`
	Column   int            `json:"column"`
	Text     string         `json:"text"`
	Spans    []Span         `json:"spans"`
	Entry    *chezmoi.Entry `json:"entry,omitempty"`
	root     string
}
type Result struct {
	Matches   []Match  `json:"matches"`
	Truncated bool     `json:"truncated"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

const maxMatches = 1000

// Search preserves partial matches alongside errors. Ripgrep exit 1 is a normal
// empty search; exit 2 and invalid regexes are errors, never false empty success.
func (s *Service) Search(ctx context.Context, owner *chezmoi.Service, query Query) (Result, error) {
	result := Result{Matches: []Match{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if query.Scope != "source" && query.Scope != "current" {
		return result, fmt.Errorf("search scope must be source or current")
	}
	if query.Text == "" {
		return result, nil
	}
	if strings.ContainsRune(query.Text, 0) || len(query.Text) > 4096 {
		return result, errors.New("search query must contain no NUL and be at most 4096 bytes")
	}
	binary, err := exec.LookPath(s.opts.RG)
	if err != nil {
		return result, fmt.Errorf("content search requires ripgrep (rg); install it or configure tools.rg: %w", err)
	}
	if owner == nil {
		return result, errors.New("content search requires an active chezmoi context")
	}
	cx, err := owner.Resolve(ctx)
	if err != nil {
		return result, err
	}
	root := cx.SourceDir
	if query.Scope == "current" {
		root = cx.DestinationDir
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return result, err
	}
	entries, err := owner.Inventory(ctx, false)
	if err != nil {
		if query.Scope == "current" {
			return result, fmt.Errorf("read managed target allowlist: %w", err)
		}
		result.Errors = append(result.Errors, "Managed-file mapping unavailable: "+err.Error())
	}
	if query.Scope == "source" {
		scripts, scriptErr := owner.Inventory(ctx, true)
		if scriptErr != nil {
			result.Errors = append(result.Errors, "Managed-script mapping unavailable: "+scriptErr.Error())
		} else {
			entries = append(entries, scripts...)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	mapping := make(map[string]*chezmoi.Entry)
	var paths []string
	for i := range entries {
		entry := entries[i]
		path := entry.Source
		if query.Scope == "current" {
			path = entry.Target
		}
		key := pathKey(path)
		if _, exists := mapping[key]; exists {
			continue
		}
		mapping[key] = &entry
		if query.Scope == "current" {
			if err := regularPath(root, path); err != nil {
				result.Skipped++
				continue
			}
			paths = append(paths, path)
		}
	}
	args := []string{"--json", "--no-config", "--color=never", "--smart-case", "--line-number", "--column", "--hidden", "--no-follow", "--no-mmap", "--encoding=none", "--sort=path"}
	if !query.Regex {
		args = append(args, "--fixed-strings")
	}
	args = append(args, "--regexp", query.Text)
	if query.Scope == "source" {
		args = append(args, "--no-require-git", "--glob=!.git", "--glob=!.specstory", "--", ".")
		err = s.run(ctx, binary, root, args, query.Scope, mapping, &result)
	} else {
		sort.Strings(paths)
		base := append(args, "--")
		cost := 0
		for _, arg := range base {
			cost += len(arg) + 1
		}
		for len(paths) > 0 && !result.Truncated {
			batch, size := []string{}, cost
			for len(paths) > 0 {
				path := paths[0]
				if cost+len(path)+1 > 8<<10 {
					result.Skipped++
					result.Errors = append(result.Errors, "Skipped an overlong managed target path")
					paths = paths[1:]
					continue
				}
				if size+len(path)+1 > 8<<10 {
					break
				}
				// Revalidate after previous batches before passing exact files.
				if regularPath(root, path) != nil {
					result.Skipped++
					paths = paths[1:]
					continue
				}
				batch, size, paths = append(batch, path), size+len(path)+1, paths[1:]
			}
			if len(batch) == 0 {
				continue
			}
			if err = s.run(ctx, binary, root, append(append([]string{}, base...), batch...), query.Scope, mapping, &result); err != nil {
				break
			}
		}
	}
	sort.SliceStable(result.Matches, func(i, j int) bool {
		a, b := result.Matches[i], result.Matches[j]
		if a.Relative != b.Relative {
			return a.Relative < b.Relative
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return result, err
}

type rgString struct {
	Text  *string `json:"text"`
	Bytes string  `json:"bytes"`
}

func (v rgString) value() (string, error) {
	if v.Text != nil {
		return *v.Text, nil
	}
	data, err := base64.StdEncoding.DecodeString(v.Bytes)
	return string(data), err
}

type event struct {
	Type string `json:"type"`
	Data struct {
		Path         rgString                   `json:"path"`
		Lines        rgString                   `json:"lines"`
		LineNumber   int                        `json:"line_number"`
		BinaryOffset *int64                     `json:"binary_offset"`
		Submatches   []struct{ Start, End int } `json:"submatches"`
	} `json:"data"`
}
type pendingFile struct {
	matches []Match
	skipped bool
	extra   bool
}

func (s *Service) run(parent context.Context, binary, root string, args []string, scope string, mapping map[string]*chezmoi.Entry, result *Result) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir, cmd.Env = root, chezmoi.ChildEnv()
	cmd.WaitDelay = 100 * time.Millisecond
	stderr := &errorBuffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ripgrep: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	pending := make(map[string]*pendingFile)
	var readErr error
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			result.Errors = append(result.Errors, "ripgrep returned invalid JSON")
			readErr = errors.New("ripgrep returned invalid JSON")
			cancel()
			break
		}
		if ev.Type != "begin" && ev.Type != "match" && ev.Type != "end" {
			continue
		}
		path, decodeErr := ev.Data.Path.value()
		if decodeErr != nil || path == "" {
			result.Errors = append(result.Errors, "ripgrep returned an invalid path")
			readErr = errors.New("ripgrep returned an invalid path")
			cancel()
			break
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		key := pathKey(path)
		p := pending[key]
		if p == nil {
			p = &pendingFile{}
			pending[key] = p
		}
		if ev.Type == "match" {
			if p.skipped {
				continue
			}
			if ev.Data.Lines.Text == nil || strings.ContainsRune(*ev.Data.Lines.Text, 0) || regularPath(root, path) != nil || scope == "current" && mapping[key] == nil {
				p.skipped = true
				p.matches = nil
				continue
			}
			text := strings.TrimSuffix(strings.TrimSuffix(*ev.Data.Lines.Text, "\n"), "\r")
			relative, _ := filepath.Rel(root, path)
			match := Match{Path: path, Relative: relative, Scope: scope, Line: ev.Data.LineNumber, Text: text, Entry: mapping[key], root: root}
			for _, span := range ev.Data.Submatches {
				if span.Start < 0 || span.End < span.Start || span.End > len(*ev.Data.Lines.Text) {
					p.skipped = true
					break
				}
				match.Spans = append(match.Spans, Span{Start: min(span.Start, len(text)), End: min(span.End, len(text))})
			}
			if p.skipped || len(match.Spans) == 0 {
				continue
			}
			match.Column = match.Spans[0].Start + 1
			if len(p.matches) < maxMatches+1 {
				p.matches = append(p.matches, match)
			} else {
				p.extra = true
			}
		}
		if ev.Type == "end" {
			if p.skipped || ev.Data.BinaryOffset != nil {
				result.Skipped++
			} else {
				for _, match := range p.matches {
					if len(result.Matches) == maxMatches {
						result.Truncated = true
						break
					}
					result.Matches = append(result.Matches, match)
				}
				if p.extra {
					result.Truncated = true
				}
			}
			delete(pending, key)
			if result.Truncated {
				cancel()
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		result.Errors = append(result.Errors, "Read ripgrep output: "+err.Error())
		readErr = fmt.Errorf("read ripgrep output: %w", err)
		cancel()
	}
	_ = stdout.Close()
	waitErr := cmd.Wait()
	if parent.Err() != nil {
		return parent.Err()
	}
	if result.Truncated {
		return nil
	}
	if readErr != nil {
		return readErr
	}
	if stderr.Len() > 0 {
		result.Errors = append(result.Errors, strings.TrimSpace(stderr.String()))
	}
	if waitErr != nil && ctx.Err() == nil {
		var status *exec.ExitError
		if !errors.As(waitErr, &status) || status.ExitCode() != 1 {
			if stderr.Len() == 0 {
				result.Errors = append(result.Errors, "ripgrep: "+waitErr.Error())
			}
			if stderr.Len() != 0 {
				return fmt.Errorf("ripgrep search failed: %s", strings.TrimSpace(stderr.String()))
			}
			return fmt.Errorf("ripgrep search failed: %w", waitErr)
		}
	}
	return nil
}

type errorBuffer struct{ bytes.Buffer }

func (b *errorBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(remaining, len(p))])
	}
	return n, nil
}

func pathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// regularPath rejects symlink descendants, directories, devices, and paths
// outside the physical root. The root itself may be a configured symlink.
func regularPath(root, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("preview path must be absolute")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("path is outside search root")
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for i, part := range parts {
		physical = filepath.Join(physical, part)
		info, err := os.Lstat(physical)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("search does not follow symbolic links")
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return errors.New("search target is not a regular file")
		}
	}
	return nil
}

func (s *Service) Preview(ctx context.Context, match Match) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if match.root == "" || match.Scope != "source" && match.Scope != "current" {
		return "", errors.New("preview requires a result from this search service")
	}
	if match.Scope == "current" && (match.Entry == nil || pathKey(match.Entry.Target) != pathKey(match.Path)) {
		return "", errors.New("current preview requires a managed target")
	}
	if err := regularPath(match.root, match.Path); err != nil {
		return "", err
	}
	file, err := os.Open(match.Path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("preview target is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > 2<<20 {
		return "", errors.New("preview exceeds 2 MiB; open the file in your editor")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("binary file cannot be previewed as text")
	}
	return string(data), nil
}
