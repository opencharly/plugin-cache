// cache_oci.go — the OCI-transport surface of `charly cache`: push a named
// ArtifactStore (an OCI Image Layout) to a registry, and pull one back into its
// local layout. It is the operator face of the cache's registry transport.
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
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/cache"
	"github.com/opencharly/spec/spec"
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

// cacheInvokeFn is the peer-dispatch seam: it sends a request body + oci_op env
// to verb:oci and returns the reply JSON. The real implementation is
// exec.InvokeProvider; tests inject a stub so the SUCCESS path (envelope build →
// dispatch → reply decode → summary) is exercised deterministically.
type cacheInvokeFn func(ctx context.Context, body, envJSON []byte) ([]byte, error)

// transferCache resolves the named cache's layout dir and dispatches the op to
// verb:oci over the reverse channel. It threads the command's ctx (from the host
// Invoke) into the peer dispatch so the call honors host cancellation.
func transferCache(parent *CacheCmd, op, name, ref string, insecure bool) error {
	if parent == nil || parent.exec == nil {
		return fmt.Errorf("charly cache %s requires the compiled-in placement (no reverse channel to reach verb:oci)", op)
	}
	baseCtx := parent.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	invoke := func(ctx context.Context, body, envJSON []byte) ([]byte, error) {
		return parent.exec.InvokeProvider(ctx, "verb", "oci", sdk.OpRun, body, envJSON, sdk.InvokeProviderOpts{})
	}
	return transferCacheWith(baseCtx, invoke, op, name, ref, insecure)
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
	body, err := json.Marshal(spec.CacheTransferRequest{Dir: dir, Ref: ref, Insecure: insecure})
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(map[string]string{"oci_op": op})
	if err != nil {
		return err
	}
	resJSON, err := invoke(ctx, body, envJSON)
	if err != nil {
		return fmt.Errorf("cache %s: %w", op, err)
	}
	var reply spec.CacheTransferReply
	if len(resJSON) > 0 {
		if uerr := json.Unmarshal(resJSON, &reply); uerr != nil {
			return fmt.Errorf("cache %s: decode reply: %w", op, uerr)
		}
	}
	verb := "pushed"
	if op == "cache-pull" {
		verb = "pulled"
	}
	fmt.Printf("cache %q %s %s -> %s\nentries: %d\ndigest: %s\nlocal layout: %s\n",
		name, verb, ref, reply.Ref, reply.Entries, reply.Digest, dir)
	return nil
}
