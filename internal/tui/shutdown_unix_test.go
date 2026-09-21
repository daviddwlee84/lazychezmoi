//go:build !windows

package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type signalTestModel struct{}

func (signalTestModel) Init() tea.Cmd {
	return func() tea.Msg { fmt.Fprintln(os.Stdout, "ready"); return nil }
}
func (m signalTestModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (signalTestModel) View() tea.View                        { return tea.NewView("signal test") }

func TestSignalShutdownHelper(t *testing.T) {
	if os.Getenv("LAZYCHEZMOI_TEST_SIGNAL_SHUTDOWN") != "1" {
		return
	}
	// Mirror main's signal context. No terminal, chezmoi source, or user files
	// are used; only the real Tea event-loop and shutdown paths are exercised.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, err := newProgram(ctx, signalTestModel{}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer()).Run()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestSIGTERMDoesNotDeadlockContextShutdown(t *testing.T) {
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSignalShutdownHelper$")
		cmd.Env = append(os.Environ(), "LAZYCHEZMOI_TEST_SIGNAL_SHUTDOWN=1")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() || scanner.Text() != "ready" {
			cancel()
			_ = cmd.Wait()
			t.Fatal("child did not start event loop")
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			cancel()
			_ = cmd.Wait()
			t.Fatal(err)
		}
		err = cmd.Wait()
		cancel()
		if err != nil {
			t.Fatalf("SIGTERM shutdown iteration %d failed/hung: %v", i, err)
		}
	}
}
