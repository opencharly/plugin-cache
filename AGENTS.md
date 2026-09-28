# AGENTS.md — plugin-cache

Standalone plugin repo serving the `charly cache` git-ref cache operator surface
(`command:cache`, dual-placement compiled-in + external). The plugin is a Go
module at `candy/plugin-cache/` (module path
`github.com/opencharly/plugin-cache/candy/plugin-cache`); the root `charly.yml`
only declares `discover: candy` so the repo is a project and its candy is
scanned.

Canonical files:

- `candy/plugin-cache/charly.yml` — the `plugin-cache:` candy entity and the
  embedded `cache-skill:` skill entity.
- `candy/plugin-cache/plugin.go` — the command provider (`Invoke(OpRun)`,
  `CliMain`, the `CacheCmd` kong tree) and `NewProvider()`/`NewMeta()`.
- `candy/plugin-cache/cache_oci.go` — the OCI-transport `push`/`pull` leaves
  (peer-dispatch to `verb:oci`).
- `candy/plugin-cache/schema/cache.cue` — the self-contained plugin schema.
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.
- `README.md` — user overview only; never agent guidance.

## Load these skills first (R0)

- `/charly-cache:cache` — the `charly cache` command reference (projected from
  the embedded `cache-skill:` entity). Load before changing the command tree.
- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:`
  block, the `command` provider class, the per-plugin CUE-schema contract.
- `/charly-internals:git-workflow` — before any git/PR action.

## Build / validate / test

- `go build ./...` in `candy/plugin-cache/` — compile the plugin module.
- `go test ./...` in `candy/plugin-cache/` — the plugin's Go tests (the OCI
  transport + schema-serve seams).
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate.

## Modify this repo

- Edit the `plugin-cache:` candy entity, the Go source, and `schema/cache.cue`
  **together** — the schema is the single source for the plugin's served
  declaration surface.
- Keep the `cache-skill:` entity in step with any command-tree change — it is the
  projected source for `/charly-cache:cache`.

## Landing

- PR-only. Every change lands through a pull request; the org-required
  `charly/pr-validator` validates the diff and body and arms native auto-merge on
  PASS. Direct pushes to `main` are blocked.
- History lives in `CHANGELOG/` (written by `tag-on-merge` at merge time); the PR
  body IS the changelog.
- The authoritative rulebook is the umbrella `AGENTS.md` in
  `opencharly/opencharly` and `charly/AGENTS.md` in the charly repo. Do not
  restate its rules here.
