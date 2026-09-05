// Command serve is the OUT-OF-PROCESS entrypoint for the cache plugin: dual-mode
// sdk.Main (serve OR CLI). charly fork/execs this binary in CLI mode for command
// dispatch (→ CliMain); the serve half exists only for the dual-mode signature. The
// SAME provider compiles INTO charly in-process (charly imports the parent package +
// registers NewProvider()/NewMeta() via plugins_generated.go) — placement-invisible (F8).
package main

import (
	cache "github.com/opencharly/plugin-cache/candy/plugin-cache"
	"github.com/opencharly/sdk"
)

func main() { sdk.Main(cache.NewProvider(), cache.NewMeta(), cache.CliMain) }
