package chezmoi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ScriptRecord identifies an exact public chezmoi state key. Descriptions are
// also compared during reset so a stale review cannot delete a newer record.
type ScriptRecord struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	Description string `json:"description"`
}

type scriptState struct {
	Name  string    `json:"name"`
	RunAt time.Time `json:"runAt"`
}
type entryState struct {
	Type           string `json:"type"`
	ContentsSHA256 string `json:"contentsSHA256"`
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (s *Service) scriptAdapter(ctx context.Context) error {
	c, err := s.Resolve(ctx)
	if err != nil {
		return err
	}
	return scriptAdapterVersion(c.Version)
}

func scriptAdapterVersion(version string) error {
	if version != "2.69.4" {
		return fmt.Errorf("granular script state is not supported for chezmoi %s (validated adapter: 2.69.4); normal script apply is available", version)
	}
	return nil
}

func (s *Service) scriptEntryState(ctx context.Context, e Entry) (entryState, error) {
	var state entryState
	data, err := s.read(ctx, "state", "get", "--bucket=entryState", "--key", e.Target)
	if err != nil {
		return state, err
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, errors.New("unrecognized script entry-state JSON")
	}
	if state.Type != "script" || !validDigest(state.ContentsSHA256) {
		return state, errors.New("selected path does not have a recognized script entry-state record")
	}
	return state, nil
}

func (s *Service) ScriptRecords(ctx context.Context, e Entry) ([]ScriptRecord, error) {
	if !strings.HasPrefix(e.Kind, "script") {
		return nil, errors.New("select a managed script before resetting history")
	}
	if err := s.scriptAdapter(ctx); err != nil {
		return nil, err
	}
	if e.Kind == "script" {
		return nil, nil
	}
	last, err := s.scriptEntryState(ctx, e)
	if err != nil {
		return nil, err
	}
	if e.Kind == "script-onchange" {
		if last.Type == "" {
			return nil, nil
		}
		return []ScriptRecord{{Bucket: "entryState", Key: e.Target, Description: "Last successful content " + last.ContentsSHA256}}, nil
	}
	if e.Kind != "script-once" {
		return nil, errors.New("unrecognized script condition")
	}
	data, err := s.read(ctx, "state", "get-bucket", "--bucket=scriptState", "--format=json")
	if err != nil {
		return nil, err
	}
	var all map[string]scriptState
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, errors.New("unrecognized script-state JSON")
	}
	// cat is used only to supplement recorded history. It is not assumed to be
	// identical to apply: templates may inspect .chezmoi.command or the clock.
	rendered, renderErr := s.read(ctx, "cat", "--", e.Target)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	current := ""
	if renderErr == nil {
		digest := sha256.Sum256(rendered)
		current = hex.EncodeToString(digest[:])
	}
	records := make([]ScriptRecord, 0)
	for key, state := range all {
		if state.Name != e.Relative && key != last.ContentsSHA256 && key != current {
			continue
		}
		if !validDigest(key) || state.Name == "" || state.RunAt.IsZero() {
			return nil, errors.New("unrecognized selected script-state record; no reset performed")
		}
		records = append(records, ScriptRecord{Bucket: "scriptState", Key: key, Description: fmt.Sprintf("%s at %s (content hash may be shared)", state.Name, state.RunAt.Format(time.RFC3339Nano))})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key < records[j].Key })
	if len(records) == 0 && renderErr != nil {
		return nil, fmt.Errorf("no recorded history could be identified and selected script rendering failed: %w", renderErr)
	}
	return records, nil
}

// ResetScript deletes only reviewed, freshly revalidated keys. This is not an
// atomic transaction with a later apply; failure can leave a partial reset.
func (s *Service) ResetScript(ctx context.Context, e Entry, records []ScriptRecord) error {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()
	if len(records) == 0 {
		return errors.New("no script history records selected")
	}
	actual, scope, err := s.locateFresh(ctx, e.Source)
	if err != nil {
		return err
	}
	if actual.ID != e.ID || actual.Kind != e.Kind {
		return errors.New("selected script changed; refresh before resetting history")
	}
	if err := scriptAdapterVersion(scope.Version); err != nil {
		return err
	}
	fresh, err := s.ScriptRecords(ctx, actual)
	if err != nil {
		return err
	}
	allowed := make(map[ScriptRecord]bool, len(fresh))
	for _, r := range fresh {
		allowed[r] = true
	}
	seen := make(map[string]bool)
	for _, r := range records {
		if !allowed[r] {
			return errors.New("script history changed or contains an unreviewed key; refresh and review again")
		}
		key := r.Bucket + "\x00" + r.Key
		if seen[key] {
			return errors.New("duplicate script history key")
		}
		seen[key] = true
	}
	for i, r := range records {
		if _, err := s.read(ctx, "state", "delete", "--bucket", r.Bucket, "--key", r.Key); err != nil {
			return fmt.Errorf("script reset stopped after %d of %d records: %w", i, len(records), err)
		}
	}
	return nil
}

// ScriptResult verifies successful execution history after the caller runs the
// command. A successful process exit alone does not mean a once script ran.
// The caller must report a process failure separately (scripts can have partial
// side effects even when no successful-execution record was written).
func (s *Service) ScriptResult(ctx context.Context, e Entry, startedAt time.Time) (string, error) {
	if err := s.scriptAdapter(ctx); err != nil {
		return "unverifiable", err
	}
	last, err := s.scriptEntryState(ctx, e)
	if err != nil {
		return "unverifiable", err
	}
	if last.Type == "" {
		return "unverifiable", nil
	}
	data, err := s.read(ctx, "state", "get", "--bucket=scriptState", "--key", last.ContentsSHA256)
	if err != nil {
		return "unverifiable", err
	}
	var record scriptState
	if len(data) == 0 || json.Unmarshal(data, &record) != nil || record.RunAt.IsZero() {
		return "unverifiable", nil
	}
	if record.Name == e.Relative && !record.RunAt.Before(startedAt) {
		return "ran", nil
	}
	return "skipped", nil
}
