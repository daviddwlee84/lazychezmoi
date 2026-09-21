package diffview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const fixtureDiff = "diff --git a/config.toml b/config.toml\n--- a/config.toml\n+++ b/config.toml\n@@ -1 +1 @@\n-name = \"before\"\n+name = \"after\"\n"

func TestMain(m *testing.M) {
	if mode := os.Getenv("LAZYCHEZMOI_TEST_DELTA"); mode != "" {
		switch mode {
		case "fail":
			fmt.Fprintln(os.Stderr, "fixture renderer failure")
			os.Exit(7)
		case "sleep":
			time.Sleep(10 * time.Second)
			os.Exit(0)
		case "controls":
			input, _ := io.ReadAll(os.Stdin)
			for _, option := range []string{"--no-gitconfig", "--paging=never", "--detect-dark-light=never", "--line-fill-method=spaces"} {
				if !strings.Contains(strings.Join(os.Args, "|"), option) {
					os.Exit(9)
				}
			}
			if os.Getenv("DELTA_FEATURES") != "" || os.Getenv("DELTA_NAVIGATE") != "" || os.Getenv("LAZYCHEZMOI_RELOAD_KIND") != "" || strings.Contains(string(input), "\x1b") {
				os.Exit(8)
			}
			fmt.Print("\x1b[31m-old\x1b[0K\x1b[0m\n\x1b]52;c;payload\x07\x1b[32m+new\x1b[3A\x1b[0m\n")
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestBuiltinWrapRetainsContent(t *testing.T) {
	raw := "-" + strings.Repeat("中文 e\u0301 😀 ", 25) + "END\n+  tabs\tstay\n"
	for _, color := range []bool{false, true} {
		result, err := Render(context.Background(), raw, Options{Renderer: "builtin", Width: 17, Color: color})
		if err != nil {
			t.Fatal(err)
		}
		if result.Renderer != "builtin" || result.Layout != "unified" {
			t.Fatalf("%+v", result)
		}
		plain := ansi.Strip(result.Text)
		if strings.ReplaceAll(plain, "\n", "") != strings.ReplaceAll(strings.ReplaceAll(raw, "\t", "    "), "\n", "") {
			t.Fatalf("wrapped content changed: %q", plain)
		}
		for _, line := range strings.Split(result.Text, "\n") {
			if ansi.StringWidth(line) > 17 {
				t.Fatalf("line too wide: %q", line)
			}
		}
	}
}

func TestDeltaFailureFallsBackButCancelDoesNot(t *testing.T) {
	executable, _ := os.Executable()
	t.Setenv("LAZYCHEZMOI_TEST_DELTA", "fail")
	result, err := Render(context.Background(), fixtureDiff, Options{Renderer: "delta", Delta: executable, Width: 80, Color: true})
	if err != nil || result.Renderer != "builtin" || !strings.Contains(result.Warning, "fixture renderer failure") || !strings.Contains(ansi.Strip(result.Text), "before") {
		t.Fatalf("fallback: %+v %v", result, err)
	}
	t.Setenv("LAZYCHEZMOI_TEST_DELTA", "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	result, err = Render(ctx, fixtureDiff, Options{Delta: executable, Width: 80, Color: true})
	if !errors.Is(err, context.DeadlineExceeded) || result.Text != "" {
		t.Fatalf("cancel must not fallback: %+v %v", result, err)
	}
}

func TestDeltaEnvironmentAndSGROnly(t *testing.T) {
	executable, _ := os.Executable()
	t.Setenv("LAZYCHEZMOI_TEST_DELTA", "controls")
	t.Setenv("DELTA_FEATURES", "raw")
	t.Setenv("DELTA_NAVIGATE", "1")
	t.Setenv("LAZYCHEZMOI_RELOAD_KIND", "bash")
	result, err := Render(context.Background(), fixtureDiff+"\x1b]52;c;secret\x07", Options{Delta: executable, Width: 80, Color: true})
	if err != nil || result.Renderer != "delta" {
		t.Fatalf("%+v %v", result, err)
	}
	if strings.Contains(result.Text, "payload") || strings.Contains(result.Text, "\x1b]") || strings.Contains(result.Text, "[0K") || strings.Contains(result.Text, "[3A") {
		t.Fatalf("unsafe controls: %q", result.Text)
	}
	if !strings.Contains(result.Text, "\x1b[31m") || ansi.Strip(result.Text) != "-old\n+new\n" {
		t.Fatalf("colors/content lost: %q", result.Text)
	}
	for _, line := range strings.Split(strings.TrimSuffix(result.Text, "\n"), "\n") {
		if !strings.HasSuffix(line, "\x1b[0m") {
			t.Fatalf("line leaks style: %q", line)
		}
	}
}

func TestMissingAndColorDisabled(t *testing.T) {
	result, err := Render(context.Background(), fixtureDiff, Options{Delta: "missing-fixture-delta", Width: 80, Color: true})
	if err != nil || result.Renderer != "builtin" || result.Warning == "" {
		t.Fatalf("%+v %v", result, err)
	}
	result, err = Render(context.Background(), fixtureDiff, Options{Delta: "must-not-run", Width: 80, Color: false})
	if err != nil || result.Warning != "" || strings.Contains(result.Text, "\x1b") {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestInstalledDeltaLayouts(t *testing.T) {
	if _, err := exec.LookPath("delta"); err != nil {
		t.Skip("delta unavailable")
	}
	for _, width := range []int{70, 120} {
		result, err := Render(context.Background(), fixtureDiff, Options{Width: width, Color: true})
		if err != nil || result.Renderer != "delta" {
			t.Fatalf("installed delta: %+v %v", result, err)
		}
		want := "unified"
		if width >= 100 {
			want = "side-by-side"
		}
		if result.Layout != want || !strings.Contains(ansi.Strip(result.Text), "before") || !strings.Contains(ansi.Strip(result.Text), "after") {
			t.Fatalf("%+v", result)
		}
		if strings.Contains(result.Text, "\x1b[0K") {
			t.Fatal("erase sequence escaped sanitizer")
		}
	}
}
