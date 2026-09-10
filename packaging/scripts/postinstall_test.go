package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostinstallReloadsSystemdAndBestEffortSyncsPublicState(t *testing.T) {
	root := t.TempDir()
	callLog := filepath.Join(root, "calls.log")
	for _, name := range []string{"systemctl", "mihomoctl"} {
		path := filepath.Join(root, name)
		script := "#!/bin/sh\nprintf '%s %s\\n' '" + name + "' \"$*\" >> \"$MIHOMOCTL_TEST_CALL_LOG\"\nexit 1\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("./postinstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.ReplaceAll(string(script), "/usr/bin/mihomoctl", filepath.Join(root, "mihomoctl")))
	testScript := filepath.Join(root, "postinstall.sh")
	if err := os.WriteFile(testScript, script, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", testScript)
	command.Env = append(os.Environ(), "PATH="+root, "MIHOMOCTL_TEST_CALL_LOG="+callLog)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("postinstall did not tolerate migration failure: %v\n%s", err, output)
	}
	content, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"systemctl daemon-reload", "mihomoctl config sync-public-state"} {
		if !strings.Contains(string(content), call) {
			t.Fatalf("postinstall missing %q call: %s", call, content)
		}
	}
}
