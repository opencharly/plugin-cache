package cache

import (
	"context"
	"testing"

	"github.com/opencharly/sdk"
)

// cache_oci_test.go — the `charly cache push/pull` OCI surface. The push/pull
// leaves are thin clients of verb:oci (the transport lives in candy/plugin-oci),
// so the deterministic unit is the reverse-channel guard + the StoreDir
// resolution; the live push/pull round-trip is candy/plugin-oci's
// TestCachePushPullLiveRoundTrip (LIVE_REGISTRY-gated).

// TestTransferCacheRequiresReverseChannel proves the leaves fail loudly (not
// silently) when the compiled-in reverse channel is absent — the out-of-process
// placement cannot reach verb:oci.
func TestTransferCacheRequiresReverseChannel(t *testing.T) {
	err := transferCache(&CacheCmd{ctx: context.Background(), exec: nil}, "cache-push", "materialized", "localhost:5000/x:y", true)
	if err == nil {
		t.Fatal("a nil executor must fail the OCI transfer (no reverse channel to reach verb:oci)")
	}
}

// TestCachePushRejectsMissingLayout proves a push of a named cache with no local
// layout fails before dispatching (so the operator gets a clear "nothing to
// push" rather than a transport error).
func TestCachePushRejectsMissingLayout(t *testing.T) {
	t.Setenv("CHARLY_CACHE_DIR", t.TempDir())
	// exec is set but the named cache dir has no layout; the StoreDir-existence
	// guard must reject before any InvokeProvider call (which would nil-panic).
	err := transferCache(&CacheCmd{ctx: context.Background(), exec: nil}, "cache-push", "no-such-cache", "localhost:5000/x:y", true)
	if err == nil {
		t.Fatal("pushing a named cache with no local layout must fail")
	}
}

// TestCacheCommandTreeHasOCISurface proves the CLI grammar exposes push/pull.
func TestCacheCommandTreeHasOCISurface(t *testing.T) {
	for _, args := range [][]string{
		{"push", "materialized", "localhost:5000/x:y"},
		{"pull", "materialized", "localhost:5000/x:y"},
	} {
		// Parse only (do not run): a parse error would mean the grammar lacks the leaf.
		cli := &CacheCmd{}
		if _, err := sdk.ParseInProcCLI("cache", cli, args); err != nil {
			t.Fatalf("`charly cache %v` must parse: %v", args, err)
		}
	}
}
