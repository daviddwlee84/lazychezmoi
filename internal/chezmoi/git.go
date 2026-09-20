package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Service) gitCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.opts.GitBinary, append([]string{"-C", root}, args...)...)
	cmd.Env = ChildEnv()
	cmd.WaitDelay = time.Second
	return cmd
}

func (s *Service) GitStatus(ctx context.Context) (GitStatus, error) {
	var status GitStatus
	c, err := s.Resolve(ctx)
	if err != nil {
		return status, err
	}
	data, err := output(s.gitCommand(ctx, c.WorkingTree, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal"))
	if err != nil {
		return status, err
	}
	for _, record := range strings.Split(string(data), "\x00") {
		switch {
		case strings.HasPrefix(record, "# branch.head "):
			status.Branch = strings.TrimPrefix(record, "# branch.head ")
		case strings.HasPrefix(record, "# branch.upstream "):
			status.Upstream = strings.TrimPrefix(record, "# branch.upstream ")
		case strings.HasPrefix(record, "# branch.ab "):
			counts := strings.Fields(strings.TrimPrefix(record, "# branch.ab "))
			if len(counts) == 2 {
				status.Ahead, _ = strconv.Atoi(strings.TrimPrefix(counts[0], "+"))
				status.Behind, _ = strconv.Atoi(strings.TrimPrefix(counts[1], "-"))
			}
		case record != "" && !strings.HasPrefix(record, "# "):
			status.Dirty = true
		}
	}
	s.mu.Lock()
	status.FetchedAt = s.fetchedAt
	s.mu.Unlock()
	path, err := output(s.gitCommand(ctx, c.WorkingTree, "rev-parse", "--git-path", "FETCH_HEAD"))
	if err == nil {
		name := strings.TrimSpace(string(path))
		if !filepath.IsAbs(name) {
			name = filepath.Join(c.WorkingTree, name)
		}
		if info, err := os.Stat(name); err == nil && info.ModTime().After(status.FetchedAt) {
			status.FetchedAt = info.ModTime()
		}
	}
	return status, nil
}

// Fetch updates remote-tracking refs, never the worktree. Background fetches
// cannot prompt and are bounded even if a credential helper or SSH stalls.
func (s *Service) Fetch(ctx context.Context, background bool) error {
	if background {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	c, err := s.Resolve(ctx)
	if err != nil {
		return err
	}
	remote, err := s.trackingRemote(ctx, c.WorkingTree)
	if err != nil {
		return err
	}
	args := []string{"fetch", "--prune", "--", remote}
	if background {
		prefix := []string{"-c", "credential.interactive=never", "-c", "core.askPass="}
		url, err := output(s.gitCommand(ctx, c.WorkingTree, "remote", "get-url", "--", remote))
		if err != nil {
			return err
		}
		if isSSHRemote(strings.TrimSpace(string(url))) {
			custom, err := s.optionalGitConfig(ctx, c.WorkingTree, "core.sshCommand")
			if err != nil {
				return err
			}
			variant, err := s.optionalGitConfig(ctx, c.WorkingTree, "ssh.variant")
			if err != nil {
				return err
			}
			if env := os.Getenv("GIT_SSH_VARIANT"); env != "" {
				variant = env
			}
			if os.Getenv("GIT_SSH_COMMAND") != "" || os.Getenv("GIT_SSH") != "" || custom != "" || (variant != "" && variant != "ssh" && variant != "auto") {
				return fmt.Errorf("background fetch cannot guarantee noninteractive custom SSH transport; use manual fetch")
			}
			prefix = append(prefix, "-c", "core.sshCommand=ssh -oBatchMode=yes -oConnectTimeout=10")
		}
		args = append(prefix, args...)
	}
	cmd := s.gitCommand(ctx, c.WorkingTree, args...)
	if background {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "SSH_ASKPASS_REQUIRE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
	} else {
		cmd.Stdin = os.Stdin
	}
	if _, err := output(cmd); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("fetch: %w", ctx.Err())
		}
		return err
	}
	s.mu.Lock()
	s.fetchedAt = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *Service) optionalGitConfig(ctx context.Context, root, key string) (string, error) {
	value, err := output(s.gitCommand(ctx, root, "config", "--get", key))
	if err == nil {
		return strings.TrimSpace(string(value)), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return "", err
}

func (s *Service) trackingRemote(ctx context.Context, root string) (string, error) {
	branch, err := output(s.gitCommand(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD"))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("fetch requires a checked-out branch with an upstream; select a branch in lazygit")
	}
	name := strings.TrimSpace(string(branch))
	remote, remoteErr := output(s.gitCommand(ctx, root, "config", "--get", "branch."+name+".remote"))
	merge, mergeErr := output(s.gitCommand(ctx, root, "config", "--get", "branch."+name+".merge"))
	if remoteErr != nil || mergeErr != nil || len(strings.TrimSpace(string(remote))) == 0 || len(strings.TrimSpace(string(merge))) == 0 {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("branch %s has no configured upstream; configure one in lazygit before fetching", name)
	}
	return strings.TrimSpace(string(remote)), nil
}

func isSSHRemote(remote string) bool {
	if strings.HasPrefix(remote, "ssh://") || strings.HasPrefix(remote, "git+ssh://") {
		return true
	}
	if strings.Contains(remote, "://") || filepath.IsAbs(remote) {
		return false
	}
	// scp-style [user@]host:path, excluding a Windows drive-letter path.
	i := strings.IndexByte(remote, ':')
	return i > 1 && !strings.ContainsAny(remote[:i], `/\`)
}
