# Changelog

## Unreleased

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
