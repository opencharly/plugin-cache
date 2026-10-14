package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/storekit"
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

// TestCacheCommandTreeHasOCISurface proves the CLI grammar exposes push/pull/ingest.
func TestCacheCommandTreeHasOCISurface(t *testing.T) {
	for _, args := range [][]string{
		{"push", "materialized", "localhost:5000/x:y"},
		{"pull", "materialized", "localhost:5000/x:y"},
		{"ingest", "localhost:5000/x:y"},
		{"ingest", "localhost:5000/x:y", "--store", "registry", "--refresh"},
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
// → Executor.InvokeProvider — with no stub at the transferCache seam. When
// writeStaged is set it instead materializes the staged layout the request names
// and answers with that layout's top-level digest, which is what the real
// verb:oci cache-pull does.
type stubExecClient struct {
	pb.ExecutorServiceClient
	gotClass, gotWord, gotOp string
	reply                    []byte
	writeStaged              func(dir string) (string, error)
}

func (s *stubExecClient) InvokeProvider(_ context.Context, in *pb.InvokeProviderRequest, _ ...grpc.CallOption) (*pb.InvokeReply, error) {
	s.gotClass, s.gotWord, s.gotOp = in.GetClass(), in.GetReserved(), in.GetOp()
	if s.writeStaged != nil {
		var req spec.CacheTransferRequest
		if err := json.Unmarshal(in.GetParamsJson(), &req); err != nil {
			return nil, err
		}
		digest, err := s.writeStaged(req.Dir)
		if err != nil {
			return nil, err
		}
		out, err := json.Marshal(spec.CacheTransferReply{Ref: req.Ref, Digest: digest, Entries: 1})
		if err != nil {
			return nil, err
		}
		return &pb.InvokeReply{ResultJson: out}, nil
	}
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

// --- the ingest (charly#892) -------------------------------------------------

// hermeticCache pins BOTH roots the store touches: the cache root (CHARLY_CACHE_DIR)
// and the runtime root the store index's lock lives under — so the test reads and
// writes nothing outside its own temp dirs.
func hermeticCache(t *testing.T) (cacheRoot, storeDir string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CHARLY_CACHE_DIR", root)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir, err := storekit.StoreDir("registry")
	if err != nil {
		t.Fatal(err)
	}
	return root, dir
}

// stagedFetcher is the injected verb:oci seam: it writes the OCI layout a real
// cache-pull would write into req.Dir (a child manifest blob + the index that
// references it) and reports the digest of the top-level artifact, counting its
// invocations so a test can prove a fetch did or did not happen.
type stagedFetcher struct {
	calls    int
	failWith error
}

// payload is the child manifest the staged index references. Its bytes are arbitrary:
// the store is content-addressed, so the test only needs them stable.
func (s *stagedFetcher) write(t *testing.T, dir string) (string, string) {
	t.Helper()
	child := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `","size":1},"layers":[]}`)
	childDigest, err := storekit.DigestOf(child)
	if err != nil {
		t.Fatal(err)
	}
	blobDir := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, strings.TrimPrefix(childDigest, "sha256:")), child, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","size":` + strconv.Itoa(len(child)) + `,"digest":"` + childDigest + `"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), idx, 0o644); err != nil {
		t.Fatal(err)
	}
	idxDigest, err := storekit.DigestOf(idx)
	if err != nil {
		t.Fatal(err)
	}
	return idxDigest, childDigest
}

func (s *stagedFetcher) invoke(t *testing.T) cacheInvokeFn {
	return func(_ context.Context, body, envJSON []byte) ([]byte, error) {
		s.calls++
		if s.failWith != nil {
			return nil, s.failWith
		}
		var req spec.CacheTransferRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		if string(envJSON) != `{"oci_op":"cache-pull"}` {
			return nil, fmt.Errorf("env = %s, want the cache-pull discriminator", envJSON)
		}
		digest, _ := s.write(t, req.Dir)
		return json.Marshal(spec.CacheTransferReply{Ref: req.Ref, Digest: digest, Entries: 1})
	}
}

// TestCacheIngestResolvesOnceAndWritesOnce is D-STORE-5 asserted in BOTH halves, each by
// a measured count rather than a claim: a repeated TAG request does no second FETCH (the
// dispatch count stays 1), and a re-run whose digest is unchanged writes no second BLOB
// (the store's blob set is identical), even when it does re-fetch (--refresh).
func TestCacheIngestResolvesOnceAndWritesOnce(t *testing.T) {
	_, storeDir := hermeticCache(t)
	st, err := storekit.OpenStore("registry")
	if err != nil {
		t.Fatal(err)
	}
	f := &stagedFetcher{}
	const ref = "quay.io/kubevirt/virt-operator:v1.9.0"

	if err := ingestCacheWith(context.Background(), f.invoke(t), ref, "registry", true, false); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("first ingest dispatched %d times, want 1", f.calls)
	}
	blobsAfterFirst, err := st.Blobs()
	if err != nil {
		t.Fatal(err)
	}
	// Three blobs, each named: the child manifest and the index the fetch staged, plus the
	// ENTRY manifest storekit publishes for the key (PutEntry marshals the entry and
	// PutBlobs it, then indexes it — measured, not assumed).
	if len(blobsAfterFirst) != 3 {
		t.Fatalf("store holds %d blobs after the first ingest, want 3 (child manifest + index + the entry manifest): %v", len(blobsAfterFirst), blobsAfterFirst)
	}
	entry, ok, err := st.LookupEntry(ref)
	if err != nil || !ok {
		t.Fatalf("the ref must be keyed in the store (ok=%v err=%v)", ok, err)
	}
	if entry.MediaType != "application/vnd.oci.image.index.v1+json" {
		t.Fatalf("entry media type = %q, want the top-level index media type", entry.MediaType)
	}
	if entry.Annotations[AnnotationRegistryRepo] != "quay.io/kubevirt/virt-operator" || entry.Annotations[AnnotationRegistryTag] != "v1.9.0" {
		t.Fatalf("annotations = %v, want the repo and the tag split out of the ref", entry.Annotations)
	}
	if entry.Annotations[AnnotationRegistryResolved] != entry.Payload {
		t.Fatalf("the D-STORE-5 resolution record (%q) must name the digest the tag resolved to (%q)", entry.Annotations[AnnotationRegistryResolved], entry.Payload)
	}
	if _, err := st.ReadBlob(entry.Payload); err != nil {
		t.Fatalf("the entry's payload must already be published in the store: %v", err)
	}

	// HALF 1 — the same tag again, upstream unreachable: no fetch, same blobs.
	f.failWith = fmt.Errorf("upstream is gone")
	if err := ingestCacheWith(context.Background(), f.invoke(t), ref, "registry", true, false); err != nil {
		t.Fatalf("a repeated tag request must be answered by the store, not the network: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("a repeated tag request dispatched %d times, want 1 (no second fetch)", f.calls)
	}

	// HALF 2 — re-resolve with --refresh: a fetch happens, no blob is written again.
	f.failWith = nil
	if err := ingestCacheWith(context.Background(), f.invoke(t), ref, "registry", true, true); err != nil {
		t.Fatalf("re-ingest with --refresh: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("--refresh dispatched %d times, want 2 (the refresh must really re-resolve)", f.calls)
	}
	blobsAfterRefresh, err := st.Blobs()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(blobsAfterFirst, ",") != strings.Join(blobsAfterRefresh, ",") {
		t.Fatalf("an unchanged digest rewrote the store's blobs:\n before %v\n after  %v", blobsAfterFirst, blobsAfterRefresh)
	}
	if staged, err := os.ReadDir(storeDir); err != nil {
		t.Fatal(err)
	} else {
		for _, e := range staged {
			if strings.HasPrefix(e.Name(), ".stage-") {
				t.Fatalf("the staging layout %s survived the ingest (it must not be left in the cache root)", e.Name())
			}
		}
	}
}

// TestCacheIngestRequiresReverseChannel proves the ingest fails loudly (not silently, and
// not by panicking on a nil seam) when the compiled-in reverse channel is absent — the
// out-of-process placement cannot reach verb:oci.
func TestCacheIngestRequiresReverseChannel(t *testing.T) {
	hermeticCache(t)
	err := ingestCacheWith(context.Background(), nil, "quay.io/x/y:v1", "registry", true, false)
	if err == nil {
		t.Fatal("a nil dispatch seam must fail the ingest")
	}
	if !strings.Contains(err.Error(), "compiled-in placement") {
		t.Fatalf("a nil seam must be refused with the placement message, got: %v", err)
	}
	var cmd *CacheCmd
	if _, _, gerr := cmd.dispatchSeam("ingest"); gerr == nil || !strings.Contains(gerr.Error(), "compiled-in placement") {
		t.Fatalf("the ingest leaf must refuse the out-of-process placement, got: %v", gerr)
	}
}

// TestStagedArtifactOfWrapperShape covers the OTHER staged shape the transport can write
// (the AppendIndex/AppendImage wrapper, where index.json holds one descriptor and the
// artifact itself is a blob): the root is that descriptor, and its bytes are already
// published, so the ingest must not PutBlob a second copy of index.json.
func TestStagedArtifactOfWrapperShape(t *testing.T) {
	dir := t.TempDir()
	root := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	rootDigest, err := storekit.DigestOf(root)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := `{
   "schemaVersion": 2,
   "mediaType": "application/vnd.oci.image.index.v1+json",
   "manifests": [{"mediaType":"application/vnd.oci.image.index.v1+json","size":` + strconv.Itoa(len(root)) + `,"digest":"` + rootDigest + `"}]
}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(wrapper), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := stagedArtifactOf(dir, rootDigest)
	if err != nil {
		t.Fatalf("wrapper shape: %v", err)
	}
	if got.digest != rootDigest || got.bytes != nil {
		t.Fatalf("wrapper shape resolved to %+v, want digest %s with no extra bytes to publish", got, rootDigest)
	}
	if _, err := stagedArtifactOf(dir, "sha256:"+strings.Repeat("f", 64)); err == nil {
		t.Fatal("a reported digest the stage does not hold must fail loudly, not guess")
	}
	if _, err := stagedArtifactOf(dir, ""); err == nil {
		t.Fatal("a transport that reports no digest must fail loudly")
	}
}

// TestSplitRef pins the annotation split, including the colon ambiguity a registry ref
// carries: a PORT colon belongs to the host, a TAG colon to the last path element.
func TestSplitRef(t *testing.T) {
	for _, tc := range []struct{ ref, repo, tag, dgst string }{
		{"quay.io/kubevirt/virt-operator:v1.9.0", "quay.io/kubevirt/virt-operator", "v1.9.0", ""},
		{"localhost:5000/x/y:t", "localhost:5000/x/y", "t", ""},
		{"localhost:5000/x/y", "localhost:5000/x/y", "", ""},
		{"docker.io/library/registry@" + "sha256:" + strings.Repeat("b", 64), "docker.io/library/registry", "", "sha256:" + strings.Repeat("b", 64)},
		{"ghcr.io/opencharly/charly-fedora:2026.279.1140", "ghcr.io/opencharly/charly-fedora", "2026.279.1140", ""},
	} {
		repo, tag, dgst := splitRef(tc.ref)
		if repo != tc.repo || tag != tc.tag || dgst != tc.dgst {
			t.Fatalf("splitRef(%q) = (%q,%q,%q), want (%q,%q,%q)", tc.ref, repo, tag, dgst, tc.repo, tc.tag, tc.dgst)
		}
	}
}

// TestCacheIngestRealDispatch drives the SHIPPED seam end to end: the ingest leaf's
// dispatchSeam closure carries the cache-pull envelope to verb:oci through an in-proc
// Executor (the compiled-in placement's real path), so nothing here is stubbed at the
// transport boundary.
func TestCacheIngestRealDispatch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_CACHE_DIR", root)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := &stagedFetcher{}
	client := &stubExecClient{}
	// The stub client must answer with the digest of the layout it just wrote, so it
	// delegates to the same staged writer the injected seam uses.
	client.writeStaged = func(dir string) (string, error) {
		digest, _ := f.write(t, dir)
		return digest, nil
	}
	ex := sdk.NewInProcExecutor(client)
	cmd := &CacheCmd{ctx: sdk.ContextWithExecutor(context.Background(), ex), exec: ex}
	invoke, baseCtx, err := cmd.dispatchSeam("ingest")
	if err != nil {
		t.Fatalf("dispatch seam: %v", err)
	}
	if err := ingestCacheWith(baseCtx, invoke, "quay.io/x/y:v1", "registry", true, false); err != nil {
		t.Fatalf("real dispatch ingest: %v", err)
	}
	if client.gotClass != "verb" || client.gotWord != "oci" || client.gotOp != sdk.OpRun {
		t.Fatalf("dispatch reached (%q,%q,%q), want (verb,oci,%s)", client.gotClass, client.gotWord, client.gotOp, sdk.OpRun)
	}
	st, err := storekit.OpenStore("registry")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, lerr := st.LookupEntry("quay.io/x/y:v1"); lerr != nil || !ok {
		t.Fatalf("the entry must be keyed after the shipped dispatch (ok=%v err=%v)", ok, lerr)
	}
}
