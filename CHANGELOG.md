# Changelog

## Unreleased

## 0.1.2 — 2026-09-23

- Add `upgrade --check` and reviewed `upgrade --yes` for the lazychezmoi
  executable's verified Homebrew formula, with clean JSON and post-upgrade
  version verification. This remains separate from chezmoi's existing `update`.

## Previously recorded notes

These notes were already present in the v0.1.1 source. They are retained without
retrospectively assigning a first-release version to each feature.

- Keep Fish 3.x shell reload streams and syntax compatible, and capture Windows
  source-context file identities before a path can be replaced.
- Restore the inspected Windows DACL after file replacement merges inherited
  entries, retaining full ACL comparisons and raw snapshot change detection.
- Verify release archives and stage complete, checksum-checked binary releases
  for macOS and Linux; use fixed upstream assets for the CI chezmoi fixture.

- Show managed files and Source previews before background status completes,
  preserving selection and usable rows when status is slow or fails.
- Share session metadata across file/script browsing and search, avoid the
  startup template scan, and invalidate reads after refresh or mutations.
- Add automatic Delta diff rendering with built-in fallback, width-aware layouts,
  and explicit Current/Source/Rendered labels.
- Add reviewed, snapshot-checked bidirectional hunk copies and session Undo,
  preserving receiving-file metadata; guard native re-add beneath exact directories.
- Add mouse controls, a maximized preview and live Source/Current content search.
- Add `search`, `hunks`, `copy-hunk`, renderer configuration and native terminal
  acceptance for mouse, hunk and search workflows.
- Add a local chezmoi dashboard with searchable files, template-aware previews,
  editor handoff, scoped apply, Git fetch/update and lazygit integration.
- Add native init, exact script-history reset and execution verification,
  externals refresh, JSON queries and shell completion.
- Add explicit parent-shell reload integration for Bash, Zsh, Fish and
  PowerShell, with isolated tests and macOS/Linux/Windows CI.
