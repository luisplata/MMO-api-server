// Package deploycontract pins the deployment shape a static review can
// check: the Go binary is a private plain-ws upstream, 8080 is its only
// port, and TLS plus the public listener belong to the reverse proxy.
package deploycontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readArtifact loads one deployment file from the module root.
func readArtifact(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

// requireContains fails when a required deployment token is missing.
func requireContains(t *testing.T, name, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("%s must contain %q", name, want)
		}
	}
}

// requireAbsent fails when a retired TCP/UDP or TLS token survives.
func requireAbsent(t *testing.T, name, body string, banned ...string) {
	t.Helper()
	for _, bad := range banned {
		if strings.Contains(body, bad) {
			t.Errorf("%s must not contain %q (WSS-only private upstream)", name, bad)
		}
	}
}

// TestDockerfileExposesOnlyTheWSSPort pins the image to one plaintext port.
func TestDockerfileExposesOnlyTheWSSPort(t *testing.T) {
	const name = "Dockerfile"
	body := readArtifact(t, name)
	requireContains(t, name, body,
		"EXPOSE 8080/tcp",
		`CMD ["-wss", ":8080", "-dev-auth", "true"]`,
	)
	requireAbsent(t, name, body, "8000", "8001", "-tcp", "-udp", "-cert", "-key", "-tls")
}

// TestComposeKeepsTheUpstreamPrivate pins the game container to an internal
// expose and blocks any host publication of the game port.
func TestComposeKeepsTheUpstreamPrivate(t *testing.T) {
	const name = "docker-compose.yml"
	body := readArtifact(t, name)
	requireContains(t, name, body,
		"expose:",
		`      - "8080"`,
		"      - -wss",
		"      - :8080",
		`"nc", "-z", "127.0.0.1", "8080"`,
	)
	requireAbsent(t, name, body, "ports:", "8000", "8001", "-tcp", "-udp", "8080:8080")
}

// TestProxyIsTheOnlyPublicListener pins TLS to nginx and the upstream to the
// compose service name on the private network.
func TestProxyIsTheOnlyPublicListener(t *testing.T) {
	const name = "deploy/proxy/nginx.conf"
	body := readArtifact(t, name)
	requireContains(t, name, body,
		"server mmo-server:8080;",
		"listen 443 ssl;",
		"ssl_certificate ",
		"ssl_certificate_key ",
		"proxy_set_header Origin $http_origin;",
	)
	requireAbsent(t, name, body, "8000", "8001", "listen 80;", "listen 8080")
}

// TestMainBindsAContainerReachableWSSAddress pins the default bind away from
// loopback: a proxy container reaches it over the private network.
func TestMainBindsAContainerReachableWSSAddress(t *testing.T) {
	const name = "cmd/server/main.go"
	body := readArtifact(t, name)
	requireContains(t, name, body, `flag.String("wss", ":8080"`)
	requireAbsent(t, name, body,
		"127.0.0.1:8080",
		`flag.String("tcp"`,
		`flag.String("udp"`,
		`flag.String("cert"`,
		`flag.String("key"`,
		`flag.String("tls"`,
	)
}

// TestMainLoadsCharacterData pins the P17 boot wiring: the deployed binary
// loads the committed template/character repositories and injects them into
// server.Config instead of leaving List/Create/Select unconfigured.
func TestMainLoadsCharacterData(t *testing.T) {
	const name = "cmd/server/main.go"
	body := readArtifact(t, name)
	requireContains(t, name, body,
		`flag.String("templates", "data/templates.json"`,
		`flag.String("characters", "data/characters.json"`,
		"server.LoadRepositories(",
		"repos.Templates",
		"repos.Characters",
	)
}

// TestMainLoadsCanonicalArtifacts pins the M1 boot defaults: the deployed
// binary loads the client-delivered canonical map, arena and content
// artifacts and verifies arena<->heightmap alignment before opening any
// listener. Removing one flag or loader from main.go fails CI here.
func TestMainLoadsCanonicalArtifacts(t *testing.T) {
	const name = "cmd/server/main.go"
	body := readArtifact(t, name)
	requireContains(t, name, body,
		`flag.String("map", "data/canonical/carnaval-solar-v1.heightmap"`,
		`flag.String("arena", "data/canonical/map.json"`,
		`flag.String("content", "data/canonical/content.json"`,
		"content.LoadContent(",
		"content.LoadArena(",
		"content.VerifyHeightmapFile(",
	)
}

// TestCanonicalArtifactsAreCommitted pins the vendored artifact set the
// boot flags default to. A missing file would make the fail-fast boot
// abort inside the container.
func TestCanonicalArtifactsAreCommitted(t *testing.T) {
	for _, name := range []string{
		"carnaval-solar-v1.heightmap",
		"carnaval-solar-v1.manifest",
		"map.json",
		"content.json",
	} {
		path := filepath.Join("..", "..", "data", "canonical", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("canonical artifact %s: %v", name, err)
			continue
		}
		if info.IsDir() || info.Size() == 0 {
			t.Errorf("canonical artifact %s is empty or a directory", name)
		}
	}
}

// TestDockerfileShipsCharacterData pins the runtime image to the committed
// data files the boot flags default to; without them the fail-fast boot
// would abort inside the container.
func TestDockerfileShipsCharacterData(t *testing.T) {
	const name = "Dockerfile"
	body := readArtifact(t, name)
	requireContains(t, name, body,
		"WORKDIR /app",
		"COPY --from=builder --chown=10001:10001 /src/data ./data",
	)
}

// TestGoServerOwnsNoTLSMaterial proves TLS never enters the Go process.
func TestGoServerOwnsNoTLSMaterial(t *testing.T) {
	for _, name := range []string{"cmd/server/main.go", "internal/server/server.go", "internal/server/ws.go"} {
		body := readArtifact(t, name)
		requireAbsent(t, name, body, "tls.Config", "ListenAndServeTLS", "CertFile", "KeyFile")
	}
}

// TestDeploymentDocNamesThePrivateUpstream pins the runbook to the same
// reachable upstream shapes and the WSS-only contract.
func TestDeploymentDocNamesThePrivateUpstream(t *testing.T) {
	const name = "docs/DEPLOYMENT.md"
	body := readArtifact(t, name)
	requireContains(t, name, body, "mmo-server:8080", "127.0.0.1:8080", "10.0.0.10:8080")
	requireAbsent(t, name, body, "8000", "8001", "-tcp", "-udp")
}
