# plugin-cache

The `charly cache` git-ref cache operator surface for OpenCharly — served as a
charly `command:cache` plugin (dual-placement: compiled-in + external).

The cache is the store of resolved `@github` refs charly keeps so repeated
resolutions do not re-fetch. Reach for `charly cache status` first when a
resolution looks stale; use `clear`/`refresh` to force fresh resolution, and
`bypass` when every resolution must hit the network.

## What it provides

| Capability | Surface |
|---|---|
| `command:cache` | the `charly cache` CLI — `status`, `clear`, `refresh`, `bypass`, plus the OCI-transport `push`/`pull` |

## The command

```
charly cache status         # the cache file path + entry count
charly cache clear          # drop every cached git answer — the next resolutions are fresh
charly cache refresh        # drop the cache and note the next resolution re-warms the refs
charly cache bypass         # persist the bypass — every resolution is fresh until turned off
charly cache bypass --off   # turn the bypass off — the cache resumes
charly cache push <name> <ref>   # push a named cache (an OCI layout) to a registry
charly cache pull <name> <ref>   # pull a named cache into its local OCI layout
```

The cache is the `cache:` section of the per-host `charly.yml`
(`~/.config/charly/charly.yml`) — shared on-disk state. The plugin operates on it
directly through `spec/refs`, so a plugin process reads and writes the SAME cache
the core singleton uses. The bypass is persisted in the cache's `git:` section
via `refs.GitClient.SetBypass`, so a fresh core process honors it at
construction. The OCI-transport leaves (`push`/`pull`) reach `verb:oci`
cache-push/cache-pull over the peer-dispatch leg and need the compiled-in
placement.

## How to use it

Compose the plugin candy in a box's `candy:` list:

```yaml
- '@github.com/opencharly/plugin-cache/candy/plugin-cache:<tag>'
```

## Layout

- `candy/plugin-cache/` — the plugin module: `plugin.go`, `cache_oci.go`,
  `schema/cache.cue`, `cmd/serve/main.go`.
- `charly.yml` — the root project manifest (`discover: candy`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Related

- Owning skill: `/charly-cache:cache` (projected from the embedded
  `cache-skill:` entity).
- `/charly-internals:git-workflow` — pin discipline and merged-ref-only pins.
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
