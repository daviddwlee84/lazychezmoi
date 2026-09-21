// Package diffview renders immutable unified diffs. Rendered rows are never
// patch identities: callers retain their own structural hunk model.
package diffview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

type Options struct {
	Renderer, Layout, Theme, SyntaxTheme, Delta string
	Width                                       int
	Color                                       bool
}

type Result struct {
	Text, Renderer, Layout, Warning string
}

// Render performs bounded, cancelable work and retains no content or disk cache.
func Render(ctx context.Context, raw string, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if opts.Renderer == "" {
		opts.Renderer = "auto"
	}
	if opts.Layout == "" {
		opts.Layout = "auto"
	}
	if opts.Theme == "" || opts.Theme == "auto" {
		opts.Theme = "dark"
	}
	if opts.Renderer != "auto" && opts.Renderer != "delta" && opts.Renderer != "builtin" {
		return Result{}, fmt.Errorf("unknown diff renderer %q", opts.Renderer)
	}
	if opts.Layout != "auto" && opts.Layout != "unified" && opts.Layout != "side-by-side" {
		return Result{}, fmt.Errorf("unknown diff layout %q", opts.Layout)
	}
	if opts.Theme != "dark" && opts.Theme != "light" {
		return Result{}, fmt.Errorf("unknown diff theme %q", opts.Theme)
	}
	if opts.Width <= 0 {
		opts.Width = 80
	}
	opts.Width = max(2, opts.Width)
	raw = clean(raw, false)
	layout := "unified"
	if opts.Width >= 100 && opts.Layout != "unified" {
		layout = "side-by-side"
	}
	warning := ""
	if opts.Layout == "side-by-side" && opts.Width < 100 {
		warning = "Pane is narrower than 100 columns; using unified diff"
	}
	if opts.Renderer != "builtin" && opts.Color && raw != "" {
		if len(raw) <= 2<<20 {
			text, err := delta(ctx, raw, opts, layout)
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if err == nil {
				text = sealLines(ansi.Hardwrap(clean(text, true), opts.Width, true), true)
				return Result{Text: text, Renderer: "delta", Layout: layout, Warning: warning}, nil
			}
			warning = joinWarning(warning, "Delta unavailable; using built-in diff: "+clean(err.Error(), false))
		} else {
			warning = joinWarning(warning, "Diff exceeds Delta's 2 MiB input limit; using built-in diff")
		}
	}
	text := builtin(raw, opts)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{Text: text, Renderer: "builtin", Layout: "unified", Warning: warning}, nil
}

func joinWarning(before, next string) string {
	if before == "" {
		return next
	}
	return before + "; " + next
}

func builtin(raw string, opts Options) string {
	text := raw
	if opts.Color && len(raw) <= 1<<20 {
		styleName := "dracula"
		if opts.Theme == "light" {
			styleName = "github"
		}
		if opts.SyntaxTheme != "" && styles.Registry[strings.ToLower(opts.SyntaxTheme)] != nil {
			styleName = opts.SyntaxTheme
		}
		style := styles.Get(styleName)
		if style == nil {
			style = styles.Fallback
		}
		iterator, err := chroma.Coalesce(lexers.Get("diff")).Tokenise(nil, raw)
		if err == nil {
			var out bytes.Buffer
			if formatters.Get("terminal256").Format(&out, style, iterator) == nil {
				text = out.String()
			}
		}
	}
	return sealLines(ansi.Hardwrap(clean(text, opts.Color), opts.Width, true), opts.Color)
}

func delta(ctx context.Context, raw string, opts Options, layout string) (string, error) {
	binary := opts.Delta
	if binary == "" {
		binary = "delta"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", errors.New("delta executable not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	args := []string{"--no-gitconfig", "--paging=never", "--detect-dark-light=never", "--line-fill-method=spaces", "--width=" + strconv.Itoa(opts.Width), "--true-color=never", "--tabs=4", "--keep-plus-minus-markers", "--file-style=omit", "--hunk-header-style=omit", "--hunk-header-decoration-style=none", "--max-line-length=0", "--" + opts.Theme}
	if opts.SyntaxTheme != "" {
		args = append(args, "--syntax-theme", opts.SyntaxTheme)
	}
	if layout == "side-by-side" {
		args = append(args, "--side-by-side", "--wrap-max-lines=unlimited")
	}
	cmd := exec.CommandContext(ctx, path, args...)
	for _, value := range chezmoi.ChildEnv() {
		key, _, _ := strings.Cut(value, "=")
		key = strings.ToUpper(key)
		// Color intent was resolved by the caller, including explicit --color
		// overriding NO_COLOR. Delta only runs for Color=true.
		if !strings.HasPrefix(key, "DELTA_") && key != "GIT_CONFIG_PARAMETERS" && key != "NO_COLOR" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Stdin = strings.NewReader(raw)
	stdout, stderr := &limitedBuffer{limit: 8 << 20}, &limitedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 100 * time.Millisecond
	err = cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if stdout.exceeded {
		return "", errors.New("rendered diff exceeds 8 MiB")
	}
	if err != nil {
		if stderr.Len() != 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return stdout.String(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		b.exceeded = true
		_, _ = b.Buffer.Write(p[:max(0, remaining)])
		return max(0, remaining), io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}

// clean uses the ANSI decoder to discard complete control sequences, including
// OSC/DCS payloads. Only numeric SGR from our renderer may survive.
func clean(text string, allowSGR bool) string {
	var out strings.Builder
	var state byte
	for len(text) > 0 {
		seq, width, n, next := ansi.DecodeSequence(text, state, nil)
		if n == 0 {
			break
		}
		text, state = text[n:], next
		switch {
		case width > 0 || len(seq) > 1 && utf8.ValidString(seq) && []rune(seq)[0] > 127 && !strings.ContainsRune(seq, '\x1b'):
			out.WriteString(strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, seq))
		case seq == "\n":
			out.WriteByte('\n')
		case seq == "\t":
			out.WriteString("    ")
		case allowSGR && validSGR(seq):
			out.WriteString(seq)
		}
	}
	return out.String()
}

func validSGR(seq string) bool {
	if len(seq) < 3 || len(seq) > 256 || !strings.HasPrefix(seq, "\x1b[") || seq[len(seq)-1] != 'm' {
		return false
	}
	for _, c := range seq[2 : len(seq)-1] {
		if c != ';' && c != ':' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// Close every physical line before the pane border, restoring active styles on
// wrapped continuations. This prevents backgrounds leaking into adjacent panes.
func sealLines(text string, color bool) string {
	if !color || !strings.Contains(text, "\x1b[") {
		return text
	}
	var out, active strings.Builder
	var state byte
	for len(text) > 0 {
		seq, _, n, next := ansi.DecodeSequence(text, state, nil)
		if n == 0 {
			break
		}
		text, state = text[n:], next
		if validSGR(seq) {
			params := seq[2 : len(seq)-1]
			if params == "" || params == "0" || strings.HasPrefix(params, "0;") {
				active.Reset()
			}
			if params != "" && params != "0" {
				if active.Len() > 4096 {
					active.Reset()
				}
				active.WriteString(seq)
			}
		}
		if seq == "\n" {
			out.WriteString("\x1b[0m\n")
			if text != "" {
				out.WriteString(active.String())
			}
		} else {
			out.WriteString(seq)
		}
	}
	if !strings.HasSuffix(out.String(), "\n") {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}
