package chezmoi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var errMetadataInvalidated = errors.New("chezmoi metadata changed while loading; refresh and retry")

type metadataSlot[T any] struct {
	value  T
	valid  bool
	flight *metadataFlight[T]
}
type metadataFlight[T any] struct {
	done       chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	generation uint64
	waiters    int
	finished   bool
	value      T
	err        error
}

func clearMetadataSlot[T any](slot *metadataSlot[T]) {
	if slot.flight != nil {
		slot.flight.cancel()
	}
	var zero metadataSlot[T]
	*slot = zero
}

// Shared loads are owned by their waiters, not by the first caller's deadline.
// Leaving one waiter never cancels work another waiter still needs; leaving the
// last waiter cancels and detaches the process so later callers can start anew.
func loadMetadata[T any](ctx context.Context, s *Service, slot *metadataSlot[T], cacheSuccess bool, loader func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	s.metadataMu.Lock()
	if slot.valid {
		value := slot.value
		s.metadataMu.Unlock()
		return value, nil
	}
	flight := slot.flight
	if flight == nil {
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		flight = &metadataFlight[T]{done: make(chan struct{}), ctx: workCtx, cancel: cancel, generation: s.metadataGeneration}
		slot.flight = flight
		go func(f *metadataFlight[T]) {
			value, err := loader(f.ctx)
			s.metadataMu.Lock()
			if f.generation != s.metadataGeneration || slot.flight != f {
				err = errMetadataInvalidated
			} else if err == nil && f.ctx.Err() != nil {
				err = f.ctx.Err()
			}
			f.value, f.err, f.finished = value, err, true
			if slot.flight == f {
				slot.flight = nil
				if err == nil && cacheSuccess {
					slot.value, slot.valid = value, true
				}
			}
			close(f.done)
			s.metadataMu.Unlock()
			f.cancel()
		}(flight)
	}
	flight.waiters++
	s.metadataMu.Unlock()
	defer func() {
		s.metadataMu.Lock()
		defer s.metadataMu.Unlock()
		flight.waiters--
		if flight.waiters == 0 && !flight.finished {
			flight.cancel()
			if slot.flight == flight {
				slot.flight = nil
			}
		}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return flight.value, flight.err
	}
}

type entryManifest struct {
	files, scripts                                   []Entry
	byTarget, bySource, byRelative, bySourceRelative map[string]Entry
}

func pathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (s *Service) manifest(ctx context.Context) (*entryManifest, error) {
	return loadMetadata(ctx, s, &s.manifestCache, true, s.loadManifest)
}

// Sensitive actions deliberately bypass both cached values and in-flight
// discovery. A scan that started before validation is not fresh enough. This
// neither replaces display metadata nor cancels its independent waiters.
func (s *Service) freshScopeAndManifest(ctx context.Context) (Context, *entryManifest, error) {
	s.metadataMu.Lock()
	generation := s.metadataGeneration
	s.metadataMu.Unlock()
	c, err := s.resolveContext(ctx)
	if err != nil {
		return Context{}, nil, err
	}
	m, err := s.loadManifest(ctx)
	if err != nil {
		return Context{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return Context{}, nil, err
	}
	s.metadataMu.Lock()
	changed := generation != s.metadataGeneration
	s.metadataMu.Unlock()
	if changed {
		return Context{}, nil, errMetadataInvalidated
	}
	return c, m, nil
}

func (s *Service) locateFresh(ctx context.Context, target string) (Entry, Context, error) {
	c, m, err := s.freshScopeAndManifest(ctx)
	if err != nil {
		return Entry{}, Context{}, err
	}
	e, err := m.locate(c, target)
	return e, c, err
}

func (s *Service) loadManifest(ctx context.Context) (*entryManifest, error) {
	data, err := s.read(ctx, "managed", "--include=files,symlinks,remove,scripts", "--exclude=externals", "--path-style=all", "--format=json")
	if err != nil {
		return nil, err
	}
	var paths map[string]managedPaths
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil, errors.New("chezmoi returned invalid managed-path JSON")
	}
	manifest := &entryManifest{files: []Entry{}, scripts: []Entry{}, byTarget: make(map[string]Entry), bySource: make(map[string]Entry), byRelative: make(map[string]Entry), bySourceRelative: make(map[string]Entry)}
	for relative, p := range paths {
		if p.Absolute == "" || p.SourceAbsolute == "" {
			return nil, errors.New("chezmoi managed entry lacks source or target path")
		}
		kind, template, encrypted := attributes(p.SourceRelative)
		entry := Entry{ID: p.Absolute, Target: p.Absolute, Source: p.SourceAbsolute, Relative: relative, Kind: kind, Template: template, Encrypted: encrypted, ExactAncestor: exactAncestor(p.SourceRelative), Drift: "?", Pending: "?"}
		if strings.HasPrefix(kind, "script") {
			manifest.scripts = append(manifest.scripts, entry)
		} else {
			manifest.files = append(manifest.files, entry)
		}
		manifest.byTarget[pathKey(entry.Target)] = entry
		manifest.bySource[pathKey(entry.Source)] = entry
		manifest.byRelative[pathKey(relative)] = entry
		manifest.bySourceRelative[pathKey(p.SourceRelative)] = entry
	}
	sort.Slice(manifest.files, func(i, j int) bool { return manifest.files[i].Relative < manifest.files[j].Relative })
	sort.Slice(manifest.scripts, func(i, j int) bool { return manifest.scripts[i].Relative < manifest.scripts[j].Relative })
	return manifest, nil
}

func (m *entryManifest) entries(scripts bool) []Entry {
	entries := m.files
	if scripts {
		entries = m.scripts
	}
	return append([]Entry{}, entries...)
}

func (m *entryManifest) locate(scope Context, target string) (Entry, error) {
	if strings.TrimSpace(target) == "" {
		return Entry{}, errors.New("a target path is required")
	}
	if target == "~" || strings.HasPrefix(target, "~/") || runtime.GOOS == "windows" && strings.HasPrefix(target, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return Entry{}, err
		}
		if target == "~" {
			target = home
		} else {
			target = filepath.Join(home, target[2:])
		}
	}
	if !filepath.IsAbs(target) {
		if entry, ok := m.byRelative[pathKey(target)]; ok {
			return entry, nil
		}
		if entry, ok := m.bySourceRelative[pathKey(target)]; ok {
			return entry, nil
		}
	}
	candidates := []string{target, filepath.Join(scope.DestinationDir, target), filepath.Join(scope.SourceStateDir, target), filepath.Join(scope.SourceDir, target)}
	if absolute, err := filepath.Abs(target); err == nil {
		candidates = append(candidates, absolute)
	}
	for _, candidate := range candidates {
		key := pathKey(candidate)
		if entry, ok := m.byTarget[key]; ok {
			return entry, nil
		}
		if entry, ok := m.bySource[key]; ok {
			return entry, nil
		}
	}
	return Entry{}, fmt.Errorf("%s is not a managed file or script", target)
}
