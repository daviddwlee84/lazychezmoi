//go:build !windows

package shell

import (
	"io"
	"os"
	"strconv"
	"testing"
)

func TestPipeChannel(t *testing.T) {
	clearChannel(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	t.Setenv(kindEnv, "bash")
	t.Setenv(fdEnv, strconv.Itoa(int(w.Fd())))
	if !Available() || !Available() {
		t.Fatal("checking capability must not close the inherited descriptor")
	}
	if err := Request(); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(token))
	if _, err := io.ReadFull(r, got); err != nil || string(got) != token {
		t.Fatalf("pipe request = %q; %v", got, err)
	}
	t.Setenv(fdEnv, strconv.Itoa(int(r.Fd())))
	if Available() {
		t.Fatal("read-only pipe must not authorize reload")
	}
	t.Setenv(fdEnv, "1")
	if Available() {
		t.Fatal("stdout must not be accepted as a private channel")
	}

	f, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t.Setenv(fdEnv, strconv.Itoa(int(f.Fd())))
	if Available() {
		t.Fatal("arbitrary file descriptor must not authorize reload")
	}
}
