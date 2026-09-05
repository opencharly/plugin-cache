// Package cache is the importable form of the charly COMMAND-class plugin for the
// git-ref cache operator surface, usable in BOTH placements (F8): COMPILED INTO
// charly in-process (charly imports this package + registers NewProvider()/NewMeta()
// via plugins_generated.go; `charly cache <args>` dispatches IN-PROC via
// Invoke(OpRun) — dispatchInProcCommand) OR served OUT-OF-PROCESS (the cmd/serve
// shim; charly fork/execs the binary in CLI mode → CliMain). Both placements run the
// SAME runCacheCLI effect, so the command behaves identically regardless of placement.
//
// The command operates on the git-ref cache DIRECTLY through spec/refs (the
// established plugin pattern — plugin-box/plugin-docs/plugin-doctor/plugin-loader/
// plugin-marketplace all import spec/refs): the cache lives in the per-host
// charly.yml `cache:` section, shared on-disk state, so a plugin process reads and
// writes the SAME cache the core singleton uses. The bypass (SetBypass) is
// PERSISTED in the cache: git: section, so a fresh core process honors it at
// construction — the plugin owns the whole operator surface, core keeps only the
// mechanism (spec/refs) + the singleton.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/refs"
)

const calver = "2026.248.0001"

// NewProvider returns the command provider for in-proc registration (compiled-in) or out-of-proc serving.
func NewProvider() pb.ProviderServer { return &provider{} }

// NewMeta advertises command:cache via sdk.NewMeta → BuildCapabilities so the
// COMPILED-IN path registers it as a command provider (buildUnitInProc → inprocProvider
// Class=command; the host builds its dynamic Kong grammar + dispatches Invoke(OpRun)).
// The served schema carries no #*Input def — a command's args are pass-through CLI
// tokens, not a structured plugin_input — so the capability has no InputDef.
func NewMeta() pb.PluginMetaServer {
	return sdk.NewMeta(calver,
		[]sdk.ProvidedCapability{{Class: "command", Word: "cache"}},
		nil)
}

// CliMain is the OUT-OF-PROCESS CLI-mode entry (charly fork/execs the binary with the
// pass-through tokens after `charly cache`). It runs the SAME effect as the in-proc Invoke(OpRun) path.
func CliMain(args []string) int {
	if err := runCacheCLI(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type provider struct{ pb.UnimplementedProviderServer }

// Invoke handles OpRun for the COMPILED-IN (in-proc) dispatch: decode the pass-through
// {args} and run the command effect in charly's own process. (Out-of-process dispatch
// is fork/exec → CliMain, never this gRPC path.)
func (provider) Invoke(_ context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	if req.GetOp() != sdk.OpRun {
		return nil, fmt.Errorf("cache: unsupported op %q (only %q)", req.GetOp(), sdk.OpRun)
	}
	var in struct {
		Args []string `json:"args"`
	}
	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &in); err != nil {
			return nil, fmt.Errorf("cache: decode args: %w", err)
		}
	}
	if err := runCacheCLI(in.Args); err != nil {
		return nil, err
	}
	return &pb.InvokeReply{}, nil
}

// CacheCmd is the `charly cache` CLI tree — the git-ref cache operator surface.
type CacheCmd struct {
	Status  CacheStatusCmd  `cmd:"" help:"Show the git-ref cache (path, entry count, bypass state)"`
	Clear   CacheClearCmd   `cmd:"" help:"Drop every cached git answer — the next resolutions are fresh"`
	Refresh CacheRefreshCmd `cmd:"" help:"Drop the cache and note the next resolution re-warms the refs"`
	Bypass  CacheBypassCmd  `cmd:"" help:"Persist the bypass — every resolution is fresh until turned off"`
}

// CacheStatusCmd reports the cache file path + entry count.
type CacheStatusCmd struct{}

func (c CacheStatusCmd) Run() error {
	g := refs.NewGitClient("")
	path, entries := g.CacheStatus()
	fmt.Printf("cache: %s\nentries: %d (latest tags + default branches + resolved SHAs + downloads)\n", path, entries)
	return nil
}

// CacheClearCmd drops every cached git answer (in-memory + the persisted file).
type CacheClearCmd struct{}

func (c CacheClearCmd) Run() error {
	if err := refs.NewGitClient("").ClearCache(); err != nil {
		return fmt.Errorf("clear cache: %w", err)
	}
	fmt.Println("git-ref cache cleared — the next resolution is fresh")
	return nil
}

// CacheRefreshCmd drops the cache and notes the next resolution re-warms the refs.
type CacheRefreshCmd struct{}

func (c CacheRefreshCmd) Run() error {
	if err := refs.NewGitClient("").ClearCache(); err != nil {
		return fmt.Errorf("refresh cache: %w", err)
	}
	fmt.Println("git-ref cache dropped — the next resolution re-warms the refs")
	return nil
}

// CacheBypassCmd persists the bypass flag (SetBypass) — every resolution is fresh
// until turned off. The flag lives in the cache: git: section, so a fresh core
// process honors it at construction.
type CacheBypassCmd struct {
	Off bool `name:"off" help:"Turn the bypass off — the cache resumes"`
}

func (c CacheBypassCmd) Run() error {
	if err := refs.NewGitClient("").SetBypass(!c.Off); err != nil {
		return fmt.Errorf("bypass: %w", err)
	}
	if c.Off {
		fmt.Println("git-ref cache bypass cleared — the cache resumes")
	} else {
		fmt.Println("git-ref cache bypassed — every resolution is fresh until `charly cache bypass --off`")
	}
	return nil
}

// runCacheCLI is the command's ONE effect, shared by both placements: kong-parse the
// pass-through args into the CacheCmd tree and run the selected leaf.
func runCacheCLI(args []string) error {
	var cli CacheCmd
	return sdk.RunInProcCLI("cache", &cli, args)
}
