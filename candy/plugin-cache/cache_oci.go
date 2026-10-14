// cache_oci.go — the OCI-transport surface of `charly cache`: push a named
// ArtifactStore (an OCI Image Layout) to a registry, pull one back into its local
// layout, and INGEST one remote ref into a named store so charly's own registry can
// serve exactly what charly ingested (charly#892).
//
// The transport itself lives in candy/plugin-oci (verb:oci cache-push/cache-pull)
// — the go-containerregistry stack is single-homed there, never linked into this
// command plugin or into core. These leaves build the SAME spec.CacheTransferRequest
// envelope plugin-oci's legs decode and reach it over the F10 peer-dispatch leg
// (InvokeProvider "verb"/"oci"), exactly like candy/plugin-box reaches verb:oci
// for its merge. The wire pair is CUE-single-sourced in spec/schema/oci.cue
// (spec#149), so the two plugins share ONE type — no hand-written copy to drift.
// The named cache's directory is resolved through spec/cache's StoreDir, so the
// operator sees the same path the ArtifactStore uses.
//
// `push`/`pull` move a WHOLE named cache; `ingest` is the registry's WRITE half: it
// fetches ONE ref through the SAME route and lands it in a named store keyed by that
// ref. The store it writes is sdk/storekit's — one flat content-addressed blob
// namespace plus key-addressed entries — because the org is converging on the ONE
// store (charly#896), and the entry's annotations are the D-STORE-5 resolution record.
// `charly registry cache add` (opencharly/charly-registry) is the SECOND ingest route
// this replaces, and it is deleted once this one has merged (R2).
//
// The store ROOT is part of the claim: storekit.StoreDir resolves under the CACHE root
// (~/.cache/charly/<name>, CHARLY_CACHE_DIR the one override), whereas spec/cache's
// StoreDir resolves under the CONFIG root (~/.config/charly/cache/<name>) — the
// deviation storekit's own header names (spec#233). So `ingest` writes the reserved
// `registry` store at ~/.cache/charly/registry; `push`/`pull` keep their existing root.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/storekit"
	"github.com/opencharly/spec/cache"
	"github.com/opencharly/spec/spec"
	ociv1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// CachePushCmd pushes a named cache to a registry: `charly cache push <name> <ref>`.
type CachePushCmd struct {
	Name     string `arg:"" help:"Named cache (the ArtifactStore name, e.g. materialized)"`
	Ref      string `arg:"" help:"Registry reference to push to (host/repo:tag)"`
	Insecure bool   `name:"insecure" help:"Allow a plain-HTTP (localhost dev) registry"`
}

func (c CachePushCmd) Run(ctx *CacheCmd) error {
	return transferCache(ctx, "cache-push", c.Name, c.Ref, c.Insecure)
}

// CachePullCmd pulls a named cache from a registry: `charly cache pull <name> <ref>`.
type CachePullCmd struct {
	Name     string `arg:"" help:"Named cache (the ArtifactStore name, e.g. materialized)"`
	Ref      string `arg:"" help:"Registry reference to pull from (host/repo:tag)"`
	Insecure bool   `name:"insecure" help:"Allow a plain-HTTP (localhost dev) registry"`
}

func (c CachePullCmd) Run(ctx *CacheCmd) error {
	return transferCache(ctx, "cache-pull", c.Name, c.Ref, c.Insecure)
}

// CacheIngestCmd ingests a remote image ref into a named store:
// `charly cache ingest <ref>` lands a registry-servable artifact in the reserved
// `registry` store by default.
type CacheIngestCmd struct {
	Ref      string `arg:"" help:"Remote image ref to ingest (host/repo:tag or host/repo@sha256:…)"`
	Store    string `name:"store" default:"registry" help:"Named store to populate (the registry store by default)"`
	Insecure bool   `name:"insecure" help:"Allow a plain-HTTP (localhost dev) registry"`
	Refresh  bool   `name:"refresh" help:"Re-resolve and re-ingest even when the ref is already stored (D-STORE-5)"`
}

func (c CacheIngestCmd) Run(ctx *CacheCmd) error {
	invoke, baseCtx, err := ctx.dispatchSeam("ingest")
	if err != nil {
		return err
	}
	return ingestCacheWith(baseCtx, invoke, c.Ref, c.Store, c.Insecure, c.Refresh)
}

// The registry store's entry annotations. `repo` and `tag` are the ref split at ingest
// time, so a reader answers Repositories/Tags without parsing the key (a key may carry a
// port colon, e.g. localhost:5000/x/y:t). `resolved` is the D-STORE-5 resolution record:
// the digest a mutable name (a tag) resolved to, recorded beside the key and OUTSIDE the
// payload's digest on purpose — a payload must not carry a resolution timestamp or a name.
const (
	AnnotationRegistryRepo     = "ai.opencharly.registry.repo"
	AnnotationRegistryTag      = "ai.opencharly.registry.tag"
	AnnotationRegistryResolved = "ai.opencharly.registry.resolved"
)

// cacheInvokeFn is the peer-dispatch seam: it sends a request body + oci_op env
// to verb:oci and returns the reply JSON. The real implementation is
// exec.InvokeProvider; tests inject a stub so the SUCCESS path (envelope build →
// dispatch → reply decode → summary) is exercised deterministically.
type cacheInvokeFn func(ctx context.Context, body, envJSON []byte) ([]byte, error)

// dispatchSeam returns the peer-dispatch closure into verb:oci plus the base context, or
// the error naming the ONE placement that can reach it. Every leaf that needs the
// transport takes its seam from here, so the reverse-channel requirement has one
// statement and one message (a CLI-mode `command:` plugin is syscall.Exec'd with the
// go-plugin handshake cookie stripped, so it has no broker and no Executor).
func (c *CacheCmd) dispatchSeam(op string) (cacheInvokeFn, context.Context, error) {
	if c == nil || c.exec == nil {
		return nil, nil, fmt.Errorf("charly cache %s requires the compiled-in placement (no reverse channel to reach verb:oci)", op)
	}
	baseCtx := c.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	invoke := func(ctx context.Context, body, envJSON []byte) ([]byte, error) {
		return c.exec.InvokeProvider(ctx, "verb", "oci", sdk.OpRun, body, envJSON, sdk.InvokeProviderOpts{})
	}
	return invoke, baseCtx, nil
}

// transferCache resolves the named cache's layout dir and dispatches the op to
// verb:oci over the reverse channel. It threads the command's ctx (from the host
// Invoke) into the peer dispatch so the call honors host cancellation.
func transferCache(parent *CacheCmd, op, name, ref string, insecure bool) error {
	invoke, baseCtx, err := parent.dispatchSeam(op)
	if err != nil {
		return err
	}
	return transferCacheWith(baseCtx, invoke, op, name, ref, insecure)
}

// dispatchTransfer builds the spec.CacheTransferRequest envelope for dir+ref, dispatches
// it to verb:oci under the oci_op discriminator, and decodes the reply. It is the ONE
// envelope builder, so the whole-cache transfer and the ingest cannot drift from each
// other or from plugin-oci's decoder — and the ONE place a missing seam is refused, so no
// caller can reach a nil dispatch (the op name is the message's subject).
func dispatchTransfer(ctx context.Context, invoke cacheInvokeFn, op, dir, ref string, insecure bool) (spec.CacheTransferReply, error) {
	if invoke == nil {
		return spec.CacheTransferReply{}, fmt.Errorf("cache %s requires the compiled-in placement (no reverse channel to reach verb:oci)", op)
	}
	body, err := json.Marshal(spec.CacheTransferRequest{Dir: dir, Ref: ref, Insecure: insecure})
	if err != nil {
		return spec.CacheTransferReply{}, err
	}
	envJSON, err := json.Marshal(map[string]string{"oci_op": op})
	if err != nil {
		return spec.CacheTransferReply{}, err
	}
	resJSON, err := invoke(ctx, body, envJSON)
	if err != nil {
		return spec.CacheTransferReply{}, fmt.Errorf("cache %s: %w", op, err)
	}
	var reply spec.CacheTransferReply
	if len(resJSON) > 0 {
		if uerr := json.Unmarshal(resJSON, &reply); uerr != nil {
			return spec.CacheTransferReply{}, fmt.Errorf("cache %s: decode reply: %w", op, uerr)
		}
	}
	return reply, nil
}

// transferCacheWith is the testable core of transferCache: it resolves the
// layout dir, builds the spec.CacheTransferRequest envelope, dispatches over the
// injected seam with ctx, decodes the spec.CacheTransferReply, and prints the
// summary.
func transferCacheWith(ctx context.Context, invoke cacheInvokeFn, op, name, ref string, insecure bool) error {
	dir, err := cache.StoreDir(name)
	if err != nil {
		return fmt.Errorf("resolve cache %q dir: %w", name, err)
	}
	// A pull may create the dir; a push requires it to exist.
	if op == "cache-push" {
		if _, serr := os.Stat(dir); serr != nil {
			return fmt.Errorf("named cache %q has no local layout at %s: %w", name, dir, serr)
		}
	}
	reply, err := dispatchTransfer(ctx, invoke, op, dir, ref, insecure)
	if err != nil {
		return err
	}
	verb := "pushed"
	if op == "cache-pull" {
		verb = "pulled"
	}
	fmt.Printf("cache %q %s %s -> %s\nentries: %d\ndigest: %s\nlocal layout: %s\n",
		name, verb, ref, reply.Ref, reply.Entries, reply.Digest, dir)
	return nil
}

// ingestCacheWith is the testable core of the ingest. The order is the contract:
//
//  1. HIT by key — the entry's resolution record answers, so a repeated request does NO
//     second fetch (D-STORE-5's first half; a key-addressed store is what makes HIT/MISS
//     decidable BEFORE any network work).
//  2. fetch into a scratch layout on the store's own filesystem through the SAME route.
//  3. land the blob namespace (content-addressed: a digest already present is not written
//     again — D-STORE-5's second half), then the keyed entry LAST, so a reader never
//     observes a descriptor whose payload is not yet published.
func ingestCacheWith(ctx context.Context, invoke cacheInvokeFn, ref, storeName string, insecure, refresh bool) error {
	st, err := storekit.OpenStore(storeName)
	if err != nil {
		return fmt.Errorf("resolve store %q: %w", storeName, err)
	}
	if !refresh {
		entry, ok, lerr := st.LookupEntry(ref)
		if lerr != nil {
			return fmt.Errorf("look up %q in store %q: %w", ref, storeName, lerr)
		}
		if ok {
			fmt.Printf("store %q already holds %q -> %s (repo %s, tag %q); no fetch\n",
				storeName, ref, entry.Payload, entry.Annotations[AnnotationRegistryRepo], entry.Annotations[AnnotationRegistryTag])
			return nil
		}
	}
	stage, err := stageLayout()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	reply, err := dispatchTransfer(ctx, invoke, "cache-pull", stage, ref, insecure)
	if err != nil {
		return fmt.Errorf("cache ingest: %w", err)
	}
	root, err := stagedArtifactOf(stage, reply.Digest)
	if err != nil {
		return fmt.Errorf("cache ingest: %w", err)
	}
	if err := landBlobs(st, stage); err != nil {
		return fmt.Errorf("cache ingest: %w", err)
	}
	// A whole-index stage keeps the index ONLY in index.json (layout.Write publishes its
	// CHILDREN as blobs and never the index itself), so its bytes are published here.
	if root.bytes != nil {
		if _, perr := st.PutBlob(root.bytes); perr != nil {
			return fmt.Errorf("cache ingest: publish the root manifest: %w", perr)
		}
	}
	repo, tag, _ := splitRef(ref)
	annotations := map[string]string{
		AnnotationRegistryRepo:     repo,
		AnnotationRegistryResolved: root.digest,
	}
	if tag != "" {
		annotations[AnnotationRegistryTag] = tag
	}
	entryDigest, err := st.PutEntry(storekit.Entry{
		Key:         ref,
		MediaType:   root.mediaType,
		Payload:     root.digest,
		Annotations: annotations,
	})
	if err != nil {
		return fmt.Errorf("cache ingest: index %q in store %q: %w", ref, storeName, err)
	}
	total, err := st.Blobs()
	if err != nil {
		return fmt.Errorf("cache ingest: count the store's blobs: %w", err)
	}
	fmt.Printf("store %q ingested %q -> %s\nmedia type: %s\nblobs: %d\nentry: %s\nstore: %s\n",
		storeName, ref, root.digest, root.mediaType, len(total), entryDigest, st.Dir())
	return nil
}

// stagedArtifact is the top-level artifact a staged fetch produced: the media type and
// digest the store's entry must carry, plus its BYTES when the staged layout does not
// already hold them.
type stagedArtifact struct {
	digest    string
	mediaType string
	bytes     []byte
}

// stagedArtifactOf resolves the staged layout's root against the digest the transport
// reported. The two shapes are distinguished by MEASUREMENT, not by a guess: when
// index.json's own bytes hash to the reported digest, the staged index IS the artifact
// (the shape plugin-oci's cache-pull writes today — measured: the file's sha256 equals the
// remote index digest); otherwise the artifact is the one descriptor in index.json that
// carries the reported digest (the AppendIndex/AppendImage shape, in which index.json is a
// wrapper whose root descriptor is itself a blob).
func stagedArtifactOf(stage, reported string) (stagedArtifact, error) {
	if reported == "" {
		return stagedArtifact{}, fmt.Errorf("the transport reported no digest for the fetched ref")
	}
	raw, err := os.ReadFile(filepath.Join(stage, ociv1.ImageIndexFile))
	if err != nil {
		return stagedArtifact{}, fmt.Errorf("read the staged %s: %w", ociv1.ImageIndexFile, err)
	}
	var idx struct {
		MediaType string `json:"mediaType"`
		Manifests []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return stagedArtifact{}, fmt.Errorf("parse the staged %s: %w", ociv1.ImageIndexFile, err)
	}
	own, err := storekit.DigestOf(raw)
	if err != nil {
		return stagedArtifact{}, err
	}
	if own == reported {
		mediaType := idx.MediaType
		if mediaType == "" {
			mediaType = ociv1.MediaTypeImageIndex
		}
		return stagedArtifact{digest: own, mediaType: mediaType, bytes: raw}, nil
	}
	for _, m := range idx.Manifests {
		if m.Digest == reported {
			return stagedArtifact{digest: m.Digest, mediaType: m.MediaType}, nil
		}
	}
	return stagedArtifact{}, fmt.Errorf("the staged layout holds neither the reported root %s nor its bytes (%d descriptors)", reported, len(idx.Manifests))
}

// landBlobs publishes every blob of the staged layout into the store through the store's
// own write (PutBlob is content-addressed and idempotent, so a digest already present is
// never written a second time), VERIFYING each file against the digest that names it — a
// staged file whose bytes do not match its own path must not enter the store.
func landBlobs(st *storekit.Store, stage string) error {
	root := filepath.Join(stage, ociv1.ImageBlobsDir)
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		named := filepath.Base(filepath.Dir(rel)) + ":" + filepath.Base(rel)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		got, derr := storekit.DigestOf(data)
		if derr != nil {
			return derr
		}
		if got != named {
			return fmt.Errorf("staged blob %s does not match its content address (got %s)", rel, got)
		}
		if _, perr := st.PutBlob(data); perr != nil {
			return perr
		}
		return nil
	})
}

// stageLayout creates the scratch layout the fetch writes into. It lives under the cache
// ROOT — the same filesystem as every store, which is what makes the store's one-copy rule
// hold — and never inside a store's own tree (a store holds no locks and no non-layout
// junk; it must keep looking like the layout it is). The caller removes it.
func stageLayout() (string, error) {
	root, err := storekit.CacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve the cache root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return "", fmt.Errorf("create a staging layout under %s: %w", root, err)
	}
	return dir, nil
}

// splitRef splits a ref into its repo, its tag and its digest — the values the store
// entry's annotations carry. It is deliberately local and small: the ref GRAMMAR belongs
// to the transport's own client (go-containerregistry, single-homed in plugin-oci), and
// this needs only the two pieces the annotations record.
func splitRef(ref string) (repo, tag, dgst string) {
	repo = ref
	if i := strings.LastIndex(repo, "@"); i >= 0 {
		dgst = repo[i+1:]
		repo = repo[:i]
	}
	slash := strings.LastIndex(repo, "/")
	last := repo[slash+1:]
	if j := strings.LastIndex(last, ":"); j >= 0 {
		tag = last[j+1:]
		repo = repo[:slash+1+j]
	}
	return repo, tag, dgst
}
