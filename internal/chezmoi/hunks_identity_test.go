package chezmoi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRoutingGuardFreezesDirectoryIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	guard, err := (&routingGuard{dirs: map[string]os.FileInfo{path: nil}}).refresh()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := guard.verify(context.Background()); err == nil {
		t.Fatal("replacement directory reused a stale snapshot identity")
	}
}

func TestRoutingGuardFreezesControlFileIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chezmoi.toml")
	if err := os.WriteFile(path, []byte("same bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	guard, err := (&routingGuard{files: map[string]controlStamp{path: {}}}).refresh()
	if err != nil {
		t.Fatal(err)
	}
	before := guard.files[path].info
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same bytes\n"), before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := guard.verify(context.Background()); err == nil {
		t.Fatal("replacement control file reused a stale snapshot identity")
	}
}
