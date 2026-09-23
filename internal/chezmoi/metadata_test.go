package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func awaitMetadataWaiters[T any](t *testing.T, s *Service, slot *metadataSlot[T], count int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		s.metadataMu.Lock()
		ready := slot.flight != nil && slot.flight.waiters == count
		s.metadataMu.Unlock()
		if ready {
			return
		}
		select {
		case <-deadline:
			t.Fatal("metadata waiters did not join")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestMetadataWaitersShareLoadButCancelIndependently(t *testing.T) {
	s := New(Options{})
	var slot metadataSlot[int]
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	loader := func(ctx context.Context) (int, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := loadMetadata(ctx, s, &slot, true, loader); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() {
		value, err := loadMetadata(context.Background(), s, &slot, true, loader)
		if err == nil && value != 42 {
			err = fmt.Errorf("value %d", value)
		}
		second <- err
	}()
	awaitMetadataWaiters(t, s, &slot, 2)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if value, err := loadMetadata(context.Background(), s, &slot, true, loader); err != nil || value != 42 || calls.Load() != 1 {
		t.Fatalf("cached %d calls=%d: %v", value, calls.Load(), err)
	}
}

func TestMetadataLastCancellationAndErrorsAreNotCached(t *testing.T) {
	s := New(Options{})
	var slot metadataSlot[int]
	started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	oldFinished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := loadMetadata(ctx, s, &slot, true, func(ctx context.Context) (int, error) {
			defer close(oldFinished)
			close(started)
			<-ctx.Done()
			close(stopped)
			<-release // Simulate a process that takes time to finish cancellation.
			return 99, nil
		})
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-stopped
	failure := errors.New("temporary discovery error")
	if _, err := loadMetadata(context.Background(), s, &slot, true, func(context.Context) (int, error) { return 0, failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if value, err := loadMetadata(context.Background(), s, &slot, true, func(context.Context) (int, error) { return 17, nil }); err != nil || value != 17 {
		t.Fatalf("retry %d: %v", value, err)
	}
	close(release)
	<-oldFinished
	if value, err := loadMetadata(context.Background(), s, &slot, true, func(context.Context) (int, error) { return 0, errors.New("unexpected reload") }); err != nil || value != 17 {
		t.Fatalf("late canceled load overwrote fresh value: %d %v", value, err)
	}
}

func TestMetadataTransientLoadsCoalesceWithoutCaching(t *testing.T) {
	s := New(Options{})
	var slot metadataSlot[int]
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	loader := func(context.Context) (int, error) {
		number := calls.Add(1)
		if number == 1 {
			close(started)
			<-release
		}
		return int(number), nil
	}
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			value, err := loadMetadata(context.Background(), s, &slot, false, loader)
			if err != nil {
				value = -1
			}
			results <- value
		}()
	}
	<-started
	awaitMetadataWaiters(t, s, &slot, 2)
	close(release)
	if first, second := <-results, <-results; first != 1 || second != 1 {
		t.Fatalf("concurrent status did not share work: %d %d", first, second)
	}
	if value, err := loadMetadata(context.Background(), s, &slot, false, loader); err != nil || value != 2 {
		t.Fatalf("sequential status retained result: %d %v", value, err)
	}
}

func TestMetadataInvalidationRejectsLateSuccess(t *testing.T) {
	s := New(Options{})
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := loadMetadata(context.Background(), s, &s.contextCache, true, func(context.Context) (Context, error) { close(started); <-release; return Context{Version: "old"}, nil })
		result <- err
	}()
	<-started
	s.Invalidate()
	value, err := loadMetadata(context.Background(), s, &s.contextCache, true, func(context.Context) (Context, error) { return Context{Version: "new"}, nil })
	if err != nil || value.Version != "new" {
		t.Fatalf("replacement: %+v %v", value, err)
	}
	close(release)
	if err := <-result; !errors.Is(err, errMetadataInvalidated) {
		t.Fatalf("late result accepted: %v", err)
	}
	value, err = s.Resolve(context.Background())
	if err != nil || value.Version != "new" {
		t.Fatalf("late result poisoned cache: %+v %v", value, err)
	}
}

// Native hook counting catches extra subprocess scans without timing assertions.
func TestMetadataHookHelper(t *testing.T) {
	if os.Getenv("LAZYCHEZMOI_TEST_METADATA_HOOK") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			f, err := os.OpenFile(os.Args[i+1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(91)
			}
			_, err = f.WriteString("call\n")
			_ = f.Close()
			if err != nil {
				os.Exit(92)
			}
			os.Exit(0)
		}
	}
	os.Exit(93)
}

func installMetadataCounter(t *testing.T, f fixture, command string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAZYCHEZMOI_TEST_METADATA_HOOK", "1")
	path := filepath.Join(f.root, command+"-count")
	config, err := os.OpenFile(f.config, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(config, "\n[hooks.%s.pre]\ncommand = %s\nargs = [\"-test.run=^TestMetadataHookHelper$\", \"--\", %s]\n", command, strconv.Quote(executable), strconv.Quote(path))
	_ = config.Close()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func metadataCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "call\n")
}

func TestSharedManifestIndexesStatusAndRefresh(t *testing.T) {
	f := newFixture(t)
	f.write(t, "dot_one", "one\n")
	f.write(t, "dot_two", "two\n")
	f.write(t, "run_once_sample."+f.extension, "fixture script\n")
	managedCount := installMetadataCounter(t, f, "managed")
	statusCount := installMetadataCounter(t, f, "status")
	files, err := f.s.Inventory(context.Background(), false)
	if err != nil || len(files) != 2 {
		t.Fatalf("files: %+v %v", files, err)
	}
	if files[0].Drift != "?" || files[0].Pending != "?" {
		t.Fatal("inventory prematurely claims clean status")
	}
	files[0].Source = "caller mutation"
	scripts, err := f.s.Inventory(context.Background(), true)
	if err != nil || len(scripts) != 1 {
		t.Fatalf("scripts: %+v %v", scripts, err)
	}
	entry := requireEntry(t, f.s, ".one")
	if entry.Source == "caller mutation" {
		t.Fatal("caller mutated cached manifest")
	}
	for _, target := range []string{entry.Source, entry.Target, "dot_one", ".one"} {
		if e := requireEntry(t, f.s, target); e.ID != entry.ID {
			t.Fatalf("bad indexed lookup: %+v", e)
		}
	}
	if got := metadataCount(t, managedCount); got != 1 {
		t.Fatalf("inventory/index calls made %d scans", got)
	}
	status, err := f.s.Status(context.Background(), false)
	if err != nil || status[".one"].Pending != "A" {
		t.Fatalf("status: %+v %v", status, err)
	}
	if metadataCount(t, managedCount) != 1 {
		t.Fatal("status reran inventory")
	}
	if err := os.WriteFile(entry.Target, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status[".one"] = EntryStatus{Pending: "caller mutation"}
	fresh, err := f.s.Status(context.Background(), false)
	if err != nil || fresh[".one"].Pending == "A" || fresh[".one"].Pending == "caller mutation" {
		t.Fatalf("status cached bytes/results: %+v %v", fresh, err)
	}
	if metadataCount(t, statusCount) != 2 {
		t.Fatal("status must remain live")
	}
	f.write(t, "dot_new", "new\n")
	if _, err := f.s.Locate(context.Background(), ".new"); err == nil {
		t.Fatal("new path appeared before explicit refresh")
	}
	f.s.Invalidate()
	if e := requireEntry(t, f.s, ".new"); e.Relative != ".new" {
		t.Fatal(e)
	}
	if metadataCount(t, managedCount) != 2 {
		t.Fatal("refresh did not rebuild combined manifest once")
	}
}

func TestCommandValidatesBatchWithOneFreshManifest(t *testing.T) {
	f := newFixture(t)
	f.write(t, "dot_one", "one")
	f.write(t, "dot_two", "two")
	count := installMetadataCounter(t, f, "managed")
	if _, err := f.s.Inventory(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Command(context.Background(), Operation{Kind: "edit", Targets: []string{".one", ".two"}}); err != nil {
		t.Fatal(err)
	}
	if got := metadataCount(t, count); got != 2 {
		t.Fatalf("batch should do one display + one fresh scan, got %d", got)
	}
}

func TestFreshValidationDoesNotJoinOrCancelOlderManifest(t *testing.T) {
	f := newFixture(t)
	f.write(t, "dot_config/one", "one")
	old, err := f.s.loadManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.source, "dot_config"), filepath.Join(f.source, "exact_dot_config")); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	oldDone := make(chan error, 1)
	go func() {
		_, err := loadMetadata(context.Background(), f.s, &f.s.manifestCache, true, func(ctx context.Context) (*entryManifest, error) {
			close(started)
			select {
			case <-release:
				return old, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		oldDone <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = f.s.Command(ctx, Operation{Kind: "re-add", Targets: []string{".config/one"}})
	if err == nil || !strings.Contains(err.Error(), "exact_") {
		close(release)
		<-oldDone
		t.Fatalf("fresh validation joined/stayed stale: %v", err)
	}
	select {
	case err := <-oldDone:
		t.Fatalf("validation cancelled unrelated waiter: %v", err)
	default:
	}
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	// The display may still use its generation's manifest until Refresh.
	rows, err := f.s.Inventory(context.Background(), false)
	if err != nil || rows[0].ExactAncestor {
		t.Fatalf("validation replaced ordinary cache: %+v %v", rows, err)
	}
}

func TestLocateExactTildeDoesNotPanic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	entry := Entry{ID: home, Target: home}
	manifest := entryManifest{byTarget: map[string]Entry{pathKey(home): entry}}
	got, err := manifest.locate(Context{}, "~")
	if err != nil || got.ID != home {
		t.Fatalf("exact tilde: %+v %v", got, err)
	}
}

func TestSensitiveActionsObserveChangedManagedClassification(t *testing.T) {
	f := newFixture(t)
	plain := f.write(t, "dot_plain", "source\n")
	if err := os.WriteFile(filepath.Join(f.destination, ".plain"), []byte("current\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := requireEntry(t, f.s, plain)
	if err := os.Rename(plain, plain+".tmpl"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Hunks(context.Background(), e); err == nil {
		t.Fatal("hunks trusted cached plain-file classification")
	}
	if _, err := f.s.Command(context.Background(), Operation{Kind: "re-add", Targets: []string{e.Target}}); err == nil {
		t.Fatal("re-add trusted cached plain-file classification")
	}
	helper := f.write(t, "new_helper", "helper\n")
	if _, err := f.s.EditSearchFile(context.Background(), helper); err == nil || !strings.Contains(err.Error(), "managed search") {
		t.Fatalf("unmanaged editor trusted stale inventory: %v", err)
	}
}
