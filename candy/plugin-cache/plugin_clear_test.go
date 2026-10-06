package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClearRepoCacheRemovesFetchedTrees is the regression guard for opencharly/charly#804:
// `charly cache clear` dropped only the ref answer index, leaving the fetched repo TREES
// on disk, so a stale candy whose manifest used a retired schema shape was still read by
// the next scan/validate and false-reddened it. clearRepoCache must remove the whole repos
// cache directory (pristine trees AND the schema-keyed .view.* derived views).
func TestClearRepoCacheRemovesFetchedTrees(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_REPO_CACHE", root)

	// A pristine fetched repo + a schema-keyed derived view, the two shapes that survive.
	for _, d := range []string{
		filepath.Join(root, "github.com/opencharly/plugin-ollama@v2026.239.1609"),
		filepath.Join(root, "github.com/opencharly/plugin-ollama@v2026.239.1609.view.abc123"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("seed %s: %v", d, err)
		}
		if err := os.WriteFile(filepath.Join(d, "charly.yml"), []byte("x: {}\n"), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}

	if err := clearRepoCache(); err != nil {
		t.Fatalf("clearRepoCache: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("clearRepoCache must remove the repos cache dir %s (stat err = %v)", root, err)
	}
}

// TestClearRepoCacheNoDirIsNoOp proves an absent cache dir is a clean no-op, not an error.
func TestClearRepoCacheNoDirIsNoOp(t *testing.T) {
	t.Setenv("CHARLY_REPO_CACHE", filepath.Join(t.TempDir(), "does-not-exist"))
	if err := clearRepoCache(); err != nil {
		t.Fatalf("clearRepoCache on an absent dir must be a no-op, got: %v", err)
	}
}
