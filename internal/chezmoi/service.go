// Package chezmoi provides the shared, synchronous operations used by the CLI
// and terminal UI. It delegates source-state semantics to the installed chezmoi.
package chezmoi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Binary, GitBinary, ConfigFile, SourceDir, DestinationDir string
	WorkingTree, CacheDir, PersistentState                   string
}

type Context struct {
	SourceDir      string `json:"source_dir"`
	SourceStateDir string `json:"source_state_dir"`
	DestinationDir string `json:"destination_dir"`
	WorkingTree    string `json:"working_tree"`
	ConfigFile     string `json:"config_file"`
	Version        string `json:"version"`
}

type Entry struct {
	ID            string `json:"id"`
	Target        string `json:"target"`
	Source        string `json:"source"`
	Relative      string `json:"relative"`
	Kind          string `json:"kind"`
	Template      bool   `json:"template"`
	Encrypted     bool   `json:"encrypted"`
	ExactAncestor bool   `json:"exact_ancestor"`
	Drift         string `json:"drift"`
	Pending       string `json:"pending"`
}

type EntryStatus struct {
	Drift   string `json:"drift"`
	Pending string `json:"pending"`
}

type GitStatus struct {
	Branch    string    `json:"branch"`
	Upstream  string    `json:"upstream"`
	Ahead     int       `json:"ahead"`
	Behind    int       `json:"behind"`
	Dirty     bool      `json:"dirty"`
	FetchedAt time.Time `json:"fetched_at"`
}

type Operation struct {
	Kind                                string
	Targets                             []string
	Init, Prompt, Apply, ExcludeScripts bool
}

type Service struct {
	opts               Options
	metadataMu         sync.Mutex
	metadataGeneration uint64
	contextCache       metadataSlot[Context]
	manifestCache      metadataSlot[*entryManifest]
	statusCache        [2]metadataSlot[map[string]EntryStatus]
	mu                 sync.Mutex
	fetchedAt          time.Time
	resetMu            sync.Mutex
	copyMu             sync.Mutex
}

// New is deliberately free of filesystem, process, and network I/O.
func New(opts Options) *Service {
	if opts.Binary == "" {
		opts.Binary = "chezmoi"
	}
	if opts.GitBinary == "" {
		opts.GitBinary = "git"
	}
	return &Service{opts: opts}
}

// Invalidate starts a new metadata generation and cancels obsolete discovery.
// It never waits for subprocesses. Rendered/file contents are never cached.
func (s *Service) Invalidate() {
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	s.metadataGeneration++
	clearMetadataSlot(&s.contextCache)
	clearMetadataSlot(&s.manifestCache)
	for i := range s.statusCache {
		clearMetadataSlot(&s.statusCache[i])
	}
}

func (s *Service) flags() []string {
	args := []string{"--no-pager", "--color=false", "--progress=false"}
	for _, pair := range [][2]string{
		{"--config", s.opts.ConfigFile}, {"--source", s.opts.SourceDir},
		{"--destination", s.opts.DestinationDir}, {"--working-tree", s.opts.WorkingTree},
		{"--cache", s.opts.CacheDir}, {"--persistent-state", s.opts.PersistentState},
	} {
		if pair[1] != "" {
			args = append(args, pair[0], pair[1])
		}
	}
	return args
}

// ChildEnv prevents editors, hooks, and nested tools from inheriting ownership
// of the parent shell's reload request. The caller alone emits that request.
func ChildEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "LAZYCHEZMOI_RELOAD_") {
			env = append(env, value)
		}
	}
	return env
}

func (s *Service) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.opts.Binary, append(s.flags(), args...)...)
	cmd.Env = ChildEnv()
	cmd.WaitDelay = time.Second
	return cmd
}

type CommandError struct {
	Command string
	Stderr  string
	Err     error
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %s", e.Command, strings.TrimSpace(e.Stderr))
	}
	return fmt.Sprintf("%s: %v", e.Command, e.Err)
}
func (e *CommandError) Unwrap() error { return e.Err }

type cappedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		p = p[:max(0, remaining)]
		b.exceeded = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func output(cmd *exec.Cmd) ([]byte, error) {
	stdout := &cappedBuffer{limit: 16 << 20}
	stderr := &cappedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	configureReadProcess(cmd)
	if err := cmd.Run(); err != nil {
		return nil, &CommandError{Command: filepath.Base(cmd.Path), Stderr: stderr.String(), Err: err}
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("%s output exceeds 16 MiB", filepath.Base(cmd.Path))
	}
	return stdout.Bytes(), nil
}

func (s *Service) read(ctx context.Context, args ...string) ([]byte, error) {
	data, err := output(s.command(ctx, append([]string{"--no-tty", "--refresh-externals=never"}, args...)...))
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return data, err
}

var versionPattern = regexp.MustCompile(`(?:version\s+v?|^v?)(\d+\.\d+\.\d+(?:[-+][^,\s]+)?)`)

// Resolve projects only paths/version out of chezmoi's configuration. Raw
// configuration (which can contain secrets) is neither retained nor logged.
func (s *Service) Resolve(ctx context.Context) (Context, error) {
	return loadMetadata(ctx, s, &s.contextCache, true, s.resolveContext)
}

func (s *Service) resolveContext(ctx context.Context) (Context, error) {
	var c Context
	configFile, err := chezmoiConfigFile(s.opts.ConfigFile)
	if err != nil {
		return c, fmt.Errorf("resolve chezmoi configuration path: %w", err)
	}
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		kind string
		data []byte
		err  error
	}
	results := make(chan result, 3)
	go func() {
		data, err := s.read(probeCtx, "dump-config", "--format=json")
		results <- result{"config", data, err}
	}()
	go func() { data, err := s.read(probeCtx, "source-path"); results <- result{"source", data, err} }()
	go func() { data, err := output(s.command(probeCtx, "--version")); results <- result{"version", data, err} }()
	values := make(map[string][]byte, 3)
	for i := 0; i < 3; i++ {
		r := <-results
		if r.err != nil {
			return c, fmt.Errorf("resolve chezmoi %s: %w", r.kind, r.err)
		}
		values[r.kind] = r.data
	}
	var projection struct {
		SourceDir      string `json:"sourceDir"`
		DestinationDir string `json:"destDir"`
		WorkingTree    string `json:"workingTree"`
	}
	if err := json.Unmarshal(values["config"], &projection); err != nil {
		return c, errors.New("chezmoi returned invalid configuration JSON")
	}
	c.SourceDir, c.DestinationDir, c.WorkingTree = projection.SourceDir, projection.DestinationDir, projection.WorkingTree
	if c.SourceDir == "" || c.DestinationDir == "" {
		return c, errors.New("chezmoi configuration lacks source or destination directory")
	}
	if c.WorkingTree == "" {
		c.WorkingTree = c.SourceDir
	}
	c.SourceStateDir = strings.TrimSuffix(strings.TrimSuffix(string(values["source"]), "\n"), "\r")
	c.ConfigFile = configFile
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(string(values["version"])))
	if len(match) == 2 {
		c.Version = match[1]
	} else {
		c.Version = "unknown"
	}
	return c, nil
}

type managedPaths struct {
	Absolute       string `json:"absolute"`
	SourceAbsolute string `json:"sourceAbsolute"`
	SourceRelative string `json:"sourceRelative"`
}

// Inventory lists managed paths without evaluating file bodies for status.
func (s *Service) Inventory(ctx context.Context, scripts bool) ([]Entry, error) {
	manifest, err := s.manifest(ctx)
	if err != nil {
		return nil, err
	}
	return manifest.entries(scripts), nil
}

func exactAncestor(sourceRelative string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(sourceRelative)), "/") {
		for _, prefix := range []string{"remove_", "external_", "exact_", "private_", "readonly_", "dot_"} {
			if strings.HasPrefix(part, prefix) {
				if prefix == "exact_" {
					return true
				}
				part = strings.TrimPrefix(part, prefix)
			}
		}
	}
	return false
}

// Entries returns useful inventory even when status fails. Callers may display
// those rows with unknown status alongside the returned error.
func (s *Service) Entries(ctx context.Context, scripts bool) ([]Entry, error) {
	entries, err := s.Inventory(ctx, scripts)
	if err != nil {
		return nil, err
	}
	status, err := s.Status(ctx, scripts)
	if err != nil {
		for i := range entries {
			entries[i].Drift, entries[i].Pending = "?", "?"
		}
		return entries, err
	}
	for i := range entries {
		value := status[entries[i].Relative]
		entries[i].Drift, entries[i].Pending = value.Drift, value.Pending
	}
	return entries, nil
}

// Status evaluates deployment status without running another inventory query.
// Results are not cached; only simultaneous requests share one subprocess.
func (s *Service) Status(ctx context.Context, scripts bool) (map[string]EntryStatus, error) {
	index := 0
	if scripts {
		index = 1
	}
	values, err := loadMetadata(ctx, s, &s.statusCache[index], false, func(ctx context.Context) (map[string]EntryStatus, error) {
		include, exclude := "files,symlinks,remove", "scripts,externals"
		if scripts {
			include, exclude = "scripts", "externals"
		}
		data, err := s.read(ctx, "status", "--include="+include, "--exclude="+exclude, "--path-style=relative")
		if err != nil {
			return nil, err
		}
		result := make(map[string]EntryStatus)
		for _, line := range strings.Split(string(data), "\n") {
			if len(line) < 3 {
				continue
			}
			relative := strings.TrimSuffix(line[3:], "\r")
			result[relative] = EntryStatus{Drift: strings.TrimSpace(line[:1]), Pending: strings.TrimSpace(line[1:2])}
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	copy := make(map[string]EntryStatus, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy, nil
}

func attributes(source string) (kind string, template, encrypted bool) {
	name := filepath.Base(source)
	template = strings.HasSuffix(name, ".tmpl")
	kind = "file"
	switch {
	case strings.HasPrefix(name, "run_once_"):
		kind = "script-once"
	case strings.HasPrefix(name, "run_onchange_"):
		kind = "script-onchange"
	case strings.HasPrefix(name, "run_"):
		kind = "script"
	case strings.HasPrefix(name, "create_"):
		kind = "create"
		name = strings.TrimPrefix(name, "create_")
	case strings.HasPrefix(name, "modify_"):
		kind = "modify"
		name = strings.TrimPrefix(name, "modify_")
	case strings.HasPrefix(name, "symlink_"):
		kind = "symlink"
	case strings.HasPrefix(name, "remove_"):
		kind = "remove"
	}
	encrypted = strings.HasPrefix(name, "encrypted_")
	if encrypted {
		// Native add --encrypt --template produces .tmpl.age (or .tmpl.asc
		// for GPG), with the encryption suffix outside the template suffix.
		switch {
		case strings.HasSuffix(name, ".age"):
			name = strings.TrimSuffix(name, ".age")
		case strings.HasSuffix(name, ".asc"):
			name = strings.TrimSuffix(name, ".asc")
		}
		template = strings.HasSuffix(name, ".tmpl")
	}
	return
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Locate accepts a managed target or source path, absolute or relative to the
// corresponding root. Filesystem paths are never interpreted by a shell.
func (s *Service) Locate(ctx context.Context, target string) (Entry, error) {
	if strings.TrimSpace(target) == "" {
		return Entry{}, errors.New("a target path is required")
	}
	c, err := s.Resolve(ctx)
	if err != nil {
		return Entry{}, err
	}
	manifest, err := s.manifest(ctx)
	if err != nil {
		return Entry{}, err
	}
	return manifest.locate(c, target)
}

func readPreviewFile(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 2<<20 {
		return "", errors.New("preview exceeds 2 MiB; open the file in your editor")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("binary content cannot be previewed as text")
	}
	return string(data), nil
}

func (s *Service) Preview(ctx context.Context, e Entry, view string) (string, error) {
	switch view {
	case "source":
		return readPreviewFile(e.Source)
	case "current":
		if e.Kind == "symlink" {
			return os.Readlink(e.Target)
		}
		return readPreviewFile(e.Target)
	case "rendered":
		data, err := s.read(ctx, "cat", "--", e.Target)
		return string(data), err
	case "diff":
		data, err := s.read(ctx, "diff", "--exclude=none", "--use-builtin-diff", "--reverse=false", "--", e.Target)
		return string(data), err
	default:
		return "", fmt.Errorf("unknown preview %q", view)
	}
}

// Command builds an interactive operation. The caller owns terminal handoff,
// stdio, confirmation, and execution; building the command never performs it.
func (s *Service) Command(ctx context.Context, op Operation) (*exec.Cmd, error) {
	var args []string
	var targets []string
	if op.Kind == "fetch" {
		c, err := s.Resolve(ctx)
		if err != nil {
			return nil, err
		}
		remote, err := s.trackingRemote(ctx, c.WorkingTree)
		if err != nil {
			return nil, err
		}
		return s.gitCommand(ctx, c.WorkingTree, "fetch", "--prune", "--", remote), nil
	}
	if op.Kind == "lazygit" {
		c, err := s.Resolve(ctx)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "lazygit")
		cmd.Dir, cmd.Env = c.WorkingTree, ChildEnv()
		cmd.WaitDelay = time.Second
		return cmd, nil
	}
	switch op.Kind {
	case "apply", "edit", "re-add", "script-apply":
		if (op.Kind == "edit" || op.Kind == "re-add" || op.Kind == "script-apply") && len(op.Targets) == 0 {
			return nil, fmt.Errorf("%s requires a selected target", op.Kind)
		}
		var manifest *entryManifest
		var scope Context
		if len(op.Targets) > 0 {
			var err error
			scope, manifest, err = s.freshScopeAndManifest(ctx)
			if err != nil {
				return nil, err
			}
		}
		for _, target := range op.Targets {
			e, err := manifest.locate(scope, target)
			if err != nil {
				return nil, err
			}
			if op.Kind == "re-add" && (e.Kind != "file" || e.Template || e.Encrypted) {
				return nil, fmt.Errorf("re-add requires a plain managed file; %s is %s", e.Relative, e.Kind)
			}
			if op.Kind == "re-add" && e.ExactAncestor {
				return nil, fmt.Errorf("re-add is unavailable beneath exact_ directories because chezmoi can change siblings; copy a content hunk or edit %s instead", e.Relative)
			}
			if op.Kind == "script-apply" {
				if !strings.HasPrefix(e.Kind, "script") {
					return nil, fmt.Errorf("%s is not a script", e.Relative)
				}
				targets = append(targets, e.Source)
			} else {
				targets = append(targets, e.Target)
			}
		}
		kind := op.Kind
		if kind == "script-apply" {
			kind = "apply"
		}
		args = []string{kind}
		if op.Kind == "apply" && len(targets) > 0 {
			args = append(args, "--parent-dirs")
		}
		if op.Init && kind != "re-add" {
			args = append(args, "--init")
		}
		if op.Kind == "script-apply" {
			args = append(args, "--source-path", "--include=scripts", "--exclude=none", "--recursive=false", "--parent-dirs=false", "--refresh-externals=never")
		} else if op.ExcludeScripts || op.Kind == "re-add" {
			args = append(args, "--exclude=scripts")
		}
		if op.Kind == "edit" && op.Apply {
			args = append(args, "--apply")
		}
	case "update":
		args = []string{"update", fmt.Sprintf("--apply=%t", op.Apply), fmt.Sprintf("--init=%t", op.Init)}
		if op.ExcludeScripts {
			args = append(args, "--exclude=scripts")
		}
	case "init":
		args = []string{"init", fmt.Sprintf("--apply=%t", op.Apply)}
		if op.Prompt {
			args = append(args, "--prompt")
		}
		if len(op.Targets) > 1 {
			return nil, errors.New("init accepts at most one repository")
		}
		if len(op.Targets) == 1 {
			if op.Targets[0] == "" || strings.HasPrefix(op.Targets[0], "-") {
				return nil, errors.New("invalid init repository")
			}
			args = append(args, op.Targets[0])
		}
	case "edit-config", "edit-config-template":
		args = []string{op.Kind}
	case "source":
		args = []string{"edit"}
		if op.Apply {
			args = append(args, "--apply")
		}
		if op.Init {
			args = append(args, "--init")
		}
		if op.ExcludeScripts {
			args = append(args, "--exclude=scripts")
		}
	case "externals":
		args = []string{"apply", "--include=externals", "--exclude=scripts", "--refresh-externals=always"}
	default:
		return nil, fmt.Errorf("unknown operation %q", op.Kind)
	}
	if len(targets) > 0 {
		// Targets are already authoritative absolute paths, so they cannot be
		// interpreted as flags. Callers can still append native prompt flags.
		args = append(args, targets...)
	}
	return s.command(ctx, args...), nil
}
