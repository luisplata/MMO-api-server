// Command heightmap-validate is the pre-transfer and CI gate for the
// canonical <name>.heightmap + <name>.manifest pair (design D8, spec
// WTM-8, task 3.1).
//
// It runs the SAME checks the server runs at boot — magic + reserved
// byte, version, dimensions, metadata, body size, finite in-range
// samples, manifest sha256 + redundant metadata — by delegating to the
// shared loader (world.LoadHeightmap). There is no server dependency:
// this command imports internal/world only, so Unity devs and CI can
// validate an artifact before it ever reaches a deployment.
//
// Exit status (design D8):
//
//	0  valid — the pair passes every check
//	1  invalid — a check failed; the reason is printed to stderr
//	2  usage — wrong arguments
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/luisplata/mmo-api-server/internal/world"
)

// Exit statuses (design D8).
const (
	exitOK      = 0
	exitInvalid = 1
	exitUsage   = 2
)

const usage = `usage: heightmap-validate <map.heightmap>

Validates a canonical <name>.heightmap with its <name>.manifest sidecar
using the exact checks the server runs at boot: magic + reserved byte,
version, dimensions, metadata, body size, finite in-range samples, and
the manifest sha256 + redundant metadata cross-check.

Exit status: 0 = valid, 1 = invalid (reason on stderr), 2 = usage.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run validates the canonical pair at args[0] and returns the process
// exit code (design D8). Any failure prints the loader's wrapped reason
// to stderr — the same actionable message the server would refuse to
// boot with. Success prints the map identity (dims, cell, origin,
// y_scale, world bounds) so the operator can confirm WHICH map passed.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	path := args[0]
	hf, err := world.LoadHeightmap(path)
	if err != nil {
		fmt.Fprintf(stderr, "heightmap-validate: %s: invalid: %v\n", path, err)
		return exitInvalid
	}
	fmt.Fprintf(stdout, "valid: %s\n", path)
	fmt.Fprintf(stdout, "  dims: %dx%d, cell %.3g, origin (%.3g, %.3g), y_scale %.3g\n",
		hf.Width, hf.Height, hf.CellSize, hf.OriginX, hf.OriginZ, hf.YScale)
	maxX := hf.OriginX + float32(hf.Width-1)*hf.CellSize
	maxZ := hf.OriginZ + float32(hf.Height-1)*hf.CellSize
	fmt.Fprintf(stdout, "  bounds: x [%.3g, %.3g], z [%.3g, %.3g]\n", hf.OriginX, maxX, hf.OriginZ, maxZ)
	fmt.Fprintln(stdout, "  manifest: sha256 + redundant metadata verified")
	return exitOK
}
