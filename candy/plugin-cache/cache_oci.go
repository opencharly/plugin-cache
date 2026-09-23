// cache_oci.go — the OCI-transport surface of `charly cache`: push a named
// ArtifactStore (an OCI Image Layout) to a registry, and pull one back into its
// local layout. It is the operator face of the cache's registry transport.
//
// The transport itself lives in candy/plugin-oci (verb:oci cache-push/cache-pull)
// — the go-containerregistry stack is single-homed there, never linked into this
// command plugin or into core. These leaves build the same CacheTransferRequest
// envelope plugin-oci's legs decode and reach it over the F10 peer-dispatch leg
// (InvokeProvider "verb"/"oci"), exactly like candy/plugin-box reaches verb:oci
// for its merge. The named cache's directory is resolved through spec/cache's
// StoreDir, so the operator sees the same path the ArtifactStore uses.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/cache"
)

// cacheTransferRequest is the verb:oci cache-push/cache-pull input — the SAME
// wire shape plugin-oci's CacheTransferRequest decodes. It is declared locally so
// this command plugin needs no link to plugin-oci (peer dispatch crosses the
// interface, not the code).
type cacheTransferRequest struct {
	Dir      string `json:"dir"`
	Ref      string `json:"ref"`
	Insecure bool   `json:"insecure,omitempty"`
}

// cacheTransferReply mirrors plugin-oci's reply for the operator summary.
type cacheTransferReply struct {
	Digest  string `json:"digest"`
	Entries int    `json:"entries"`
	Ref     string `json:"ref"`
}

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

// transferCache resolves the named cache's layout dir and dispatches the op to
// verb:oci over the reverse channel.
func transferCache(parent *CacheCmd, op, name, ref string, insecure bool) error {
	if parent == nil || parent.exec == nil {
		return fmt.Errorf("charly cache %s requires the compiled-in placement (no reverse channel to reach verb:oci)", op)
	}
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
	body, err := json.Marshal(cacheTransferRequest{Dir: dir, Ref: ref, Insecure: insecure})
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(map[string]string{"oci_op": op})
	if err != nil {
		return err
	}
	resJSON, err := parent.exec.InvokeProvider(context.Background(), "verb", "oci", sdk.OpRun, body, envJSON, sdk.InvokeProviderOpts{})
	if err != nil {
		return fmt.Errorf("cache %s: %w", op, err)
	}
	var reply cacheTransferReply
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
