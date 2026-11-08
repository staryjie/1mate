package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestScriptsAreValidShell syntax-checks every generated variant with a real
// POSIX shell when one is available. It skips on hosts without sh.
func TestScriptsAreValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell available")
	}
	variants := map[string]Options{}
	repo := DefaultOptions()
	variants["repo"] = repo

	script := DefaultOptions()
	script.Strategy = StrategyScript
	variants["script"] = script

	composeOnly := DefaultOptions()
	composeOnly.Strategy = StrategyComposeOnly
	variants["compose_only"] = composeOnly

	withMirrors := DefaultOptions()
	withMirrors.Mirror = "Aliyun"
	withMirrors.RegistryMirrors = []string{"https://mirror.gcr.io"}
	withMirrors.DockerVersion = "27.3.1"
	variants["mirrors"] = withMirrors

	dir := t.TempDir()
	for name, opts := range variants {
		body, err := Script(opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join(dir, name+".sh")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(sh, "-n", path).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: shell syntax error: %v\n%s\n%s", name, err, out, body)
		}
	}
}
