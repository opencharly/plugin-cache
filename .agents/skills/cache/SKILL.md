---
name: cache
description: 'The `charly cache` git-ref cache operator surface — status, clear, refresh,
  bypass.

  Use before debugging stale @github ref resolution, a pin that will not advance,
  or

  a network-fetch that keeps returning an old answer.'
---

# charly cache — the git-ref cache operator surface

`charly cache` manages the git-ref cache: the store of resolved `@github` refs charly
keeps so repeated resolutions do not re-fetch. It is served by `candy/plugin-cache`
(a `command:cache` plugin, dual-placement compiled-in + external).

## Command tree

- `charly cache status` — the cache file path + entry count.
- `charly cache clear` — drop every cached git answer; the next resolutions are fresh.
- `charly cache refresh` — drop the cache and note the next resolution re-warms the refs.
- `charly cache bypass` — persist the bypass; every resolution is fresh until turned off.
- `charly cache bypass --off` — turn the bypass off; the cache resumes.
- `charly cache push <name> <ref>` — push a named cache (an OCI layout) to a registry.
- `charly cache pull <name> <ref>` — pull a named cache from a registry into its local layout.
- `charly cache ingest <ref>` — ingest a remote image ref into a named store (the reserved
  `registry` store by default), so charly's own registry can serve it. `--store <name>`
  chooses the store, `--insecure` allows a plain-HTTP dev registry, `--refresh` re-resolves
  even when the ref is already stored.

## Ingesting into the registry store

`charly cache ingest <ref>` is the write half of the registry capability. It resolves the
ref's artifacts through `verb:oci` (the go-containerregistry transport stays single-homed in
plugin-oci) and lands them in a named `sdk/storekit` store: every blob is written
content-addressed by digest, then ONE entry keyed by the full ref — with the repo, the tag
and the digest the tag resolved to in its annotations (D-STORE-5). A mutable name therefore
resolves ONCE: a repeated `charly cache ingest <ref>` is answered by the store with no second
fetch, and a re-run at an unchanged digest writes no second blob (`--refresh` forces the
re-resolution, not a rewrite). It needs the compiled-in placement, because the reverse
channel to `verb:oci` exists only there.

The reserved store is `registry`, under the CACHE root: `~/.cache/charly/registry`
(`CHARLY_CACHE_DIR` overrides the root). That is `storekit`'s resolver — distinct from
`charly cache push`/`pull`'s config-rooted `~/.config/charly/cache/<name>` layouts.

## Where the cache lives

The cache is the `cache:` section of the per-host `charly.yml`
(`~/.config/charly/charly.yml`) — shared on-disk state. The plugin operates on it
DIRECTLY through `spec/refs`, so a plugin process reads and writes the SAME cache the
core singleton uses. The bypass is persisted in the cache's `git:` section via
`refs.GitClient.SetBypass`, so a fresh core process honors it at construction.

## When to use it

Reach for `charly cache status` first when a `@github` resolution looks stale; use
`charly cache clear`/`refresh` to force fresh resolution, and `bypass` when every
resolution must hit the network (e.g. debugging a remote branch that advanced).

## Cross-References

- `/charly-internals:git-workflow` — pin discipline and merged-ref-only pins.
- `/charly-internals:plugin` — the command-plugin class and providers.
