# plugin-cache

The `charly cache` command — the git-ref cache operator surface, served as a charly
COMMAND-class plugin (dual-placement: compiled-in + external).

## The command

```
charly cache status    # show the cache file path + entry count
charly cache clear     # drop every cached git answer — the next resolutions are fresh
charly cache refresh   # drop the cache and note the next resolution re-warms the refs
charly cache bypass    # persist the bypass — every resolution is fresh until turned off
charly cache bypass --off  # turn the bypass off — the cache resumes
```

## Architecture

The git-ref cache lives in the `cache:` section of the per-host charly.yml
(~/.config/charly/charly.yml) — shared on-disk state. The plugin operates on it
DIRECTLY through `spec/refs` (the established plugin pattern — plugin-box,
plugin-docs, plugin-doctor, plugin-loader, plugin-marketplace all import
`spec/refs`), so a plugin process reads and writes the SAME cache the core
singleton uses.

The bypass (`charly cache bypass`) is PERSISTED in the cache: git: section via
`refs.GitClient.SetBypass` (spec #95), so a fresh core process honors it at
construction. The plugin owns the whole operator surface; core keeps only the
mechanism (`spec/refs`) + the singleton.

## Placement

- **Compiled-in** (F8): listed in charly's `compiled_plugins:` — `charly cache`
  dispatches IN-PROC via `Invoke(OpRun)` (native stdio/TTY).
- **External**: charly prescans `cache` into the CLI grammar and fork/execs this
  binary in CLI mode (`cmd/serve` → `CliMain`).

Both placements run the SAME `runCacheCLI` effect — placement-invisible.
