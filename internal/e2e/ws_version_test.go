package e2e

// P11 protocol invariant (docs/PROTOCOL.md §9): the envelope `version`
// is validated for EVERY frame, not only lifecycle frames. The WSS
// frame loop carries lifecycle and realtime on one socket, so a
// MoveInput with an out-of-range version must be answered with
// VersionMismatch and close the session — never silently applied.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestWSSMoveInputOutOfRangeVersionCloses(t *testing.T) {
	wssURL, shutdown := startProxyUpstream(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	reg := protocol.NewWorldRegistry()

	a := newWSClient(t, reg, front, proxyWSOrigin, "alice")
	a.enterWorld(t, "alice", "warrior", "hero")

	// A realtime MoveInput is valid only if its envelope version is in
	// the negotiated range. It bypasses the lifecycle state machine, so
	// the frame loop itself must enforce the version.
	a.sendVersion(t, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1}, Speed: moveSpeed, Yaw: 0}, protoVersion+1)

	vm := requireWS[*mmov1.VersionMismatch](t, a, "VersionMismatch for out-of-range MoveInput")
	if vm.MinVer != protoVersion || vm.MaxVer != protoVersion {
		t.Errorf("VersionMismatch range = [%d, %d], want [%d, %d]", vm.MinVer, vm.MaxVer, protoVersion, protoVersion)
	}
	requireTerminated(t, a)
}
