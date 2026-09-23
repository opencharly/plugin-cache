package cache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
	"google.golang.org/grpc"
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
// layout fails BEFORE dispatching, via the StoreDir-existence guard (not the
// reverse-channel guard): a non-nil executor lets transferCache reach os.Stat,
// where the missing layout is caught with a clear "nothing to push".
func TestCachePushRejectsMissingLayout(t *testing.T) {
	t.Setenv("CHARLY_CACHE_DIR", t.TempDir())
	err := transferCache(&CacheCmd{ctx: context.Background(), exec: &sdk.Executor{}}, "cache-push", "no-such-cache", "localhost:5000/x:y", true)
	if err == nil {
		t.Fatal("pushing a named cache with no local layout must fail")
	}
	if !strings.Contains(err.Error(), "no local layout") {
		t.Fatalf("expected the missing-layout guard to fire, got: %v", err)
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

// TestTransferCacheSuccessPath drives the full SUCCESS path with an injected
// dispatch seam: it resolves the layout dir, builds the spec.CacheTransferRequest
// envelope (asserting the fields), receives a spec.CacheTransferReply, and
// decodes it — the changed behaviour a broken success path would fail.
func TestTransferCacheSuccessPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_CACHE_DIR", root)
	// The push guard requires the layout dir to exist.
	if err := os.MkdirAll(filepath.Join(root, "materialized"), 0o755); err != nil {
		t.Fatal(err)
	}
	var gotBody []byte
	var gotEnv []byte
	invoke := func(_ context.Context, body, envJSON []byte) ([]byte, error) {
		gotBody, gotEnv = body, envJSON
		return json.Marshal(spec.CacheTransferReply{Digest: "sha256:abc", Entries: 3, Ref: "reg/x:tag"})
	}
	if err := transferCacheWith(context.Background(), invoke, "cache-push", "materialized", "reg/x:tag", true); err != nil {
		t.Fatalf("success path: %v", err)
	}
	var req spec.CacheTransferRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("request not a spec.CacheTransferRequest: %v", err)
	}
	if req.Dir != filepath.Join(root, "materialized") || req.Ref != "reg/x:tag" || !req.Insecure {
		t.Fatalf("request envelope = %+v (want dir=%s ref=reg/x:tag insecure)", req, filepath.Join(root, "materialized"))
	}
	if string(gotEnv) != `{"oci_op":"cache-push"}` {
		t.Fatalf("env = %s, want the cache-push discriminator", gotEnv)
	}
}

// stubExecClient is a pb.ExecutorServiceClient whose InvokeProvider returns a
// canned reply; the embedded nil interface satisfies the other methods (never
// called here). It lets a test drive the REAL dispatch — ctx → ExecutorForInvoke
// → Executor.InvokeProvider — with no stub at the transferCache seam.
type stubExecClient struct {
	pb.ExecutorServiceClient
	gotClass, gotWord, gotOp string
	reply                    []byte
}

func (s *stubExecClient) InvokeProvider(_ context.Context, in *pb.InvokeProviderRequest, _ ...grpc.CallOption) (*pb.InvokeReply, error) {
	s.gotClass, s.gotWord, s.gotOp = in.GetClass(), in.GetReserved(), in.GetOp()
	return &pb.InvokeReply{ResultJson: s.reply}, nil
}

// TestTransferCacheRealDispatch drives the REAL reverse-channel dispatch: an
// in-proc Executor built from a stub client is threaded on ctx (the compiled-in
// placement's path), transferCache resolves it via ExecutorForInvoke, and the
// real Executor.InvokeProvider("verb","oci",OpRun,…) runs — proving the shipped
// dispatch (not a transferCacheWith stub) carries the envelope to verb:oci.
func TestTransferCacheRealDispatch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_CACHE_DIR", root)
	if err := os.MkdirAll(filepath.Join(root, "materialized"), 0o755); err != nil {
		t.Fatal(err)
	}
	reply, _ := json.Marshal(spec.CacheTransferReply{Digest: "sha256:real", Entries: 2, Ref: "reg/x:tag"})
	client := &stubExecClient{reply: reply}
	ex := sdk.NewInProcExecutor(client)

	cmd := &CacheCmd{ctx: sdk.ContextWithExecutor(context.Background(), ex), exec: ex}
	if err := transferCache(cmd, "cache-push", "materialized", "reg/x:tag", true); err != nil {
		t.Fatalf("real dispatch: %v", err)
	}
	if client.gotClass != "verb" || client.gotWord != "oci" || client.gotOp != sdk.OpRun {
		t.Fatalf("dispatch reached (%q,%q,%q), want (verb,oci,%s)", client.gotClass, client.gotWord, client.gotOp, sdk.OpRun)
	}
}
