package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallSyncsPublicStateOnlyOutsideDESTDIR(t *testing.T) {
	for _, test := range []struct {
		name     string
		staged   bool
		wantSync bool
	}{
		{name: "live install", wantSync: true},
		{name: "staged package", staged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			toolsDir := filepath.Join(root, "tools")
			if err := os.MkdirAll(toolsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			fakeGo := filepath.Join(toolsDir, "go")
			goScript := `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-o" ]; then
		out="$2"
		shift 2
		continue
	fi
	shift
done
printf '%s\n' '#!/bin/sh' 'printf "%s\\n" "$*" >> "$MIHOMOCTL_TEST_CALL_LOG"' > "$out"
chmod 0755 "$out"
`
			if err := os.WriteFile(fakeGo, []byte(goScript), 0o700); err != nil {
				t.Fatal(err)
			}
			fakeSudo := filepath.Join(toolsDir, "sudo")
			if err := os.WriteFile(fakeSudo, []byte("#!/bin/sh\n[ \"$1\" = \"--\" ] && shift\nexec \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			prefix := filepath.Join(root, "prefix")
			fakeInstall := filepath.Join(toolsDir, "install")
			installScript := `#!/bin/sh
target=""
for argument in "$@"; do
	target="$argument"
done
case "$target" in
	"$MIHOMOCTL_TEST_PREFIX"|"$MIHOMOCTL_TEST_PREFIX"/*)
		exec /usr/bin/install "$@"
		;;
esac
exit 0
`
			if err := os.WriteFile(fakeInstall, []byte(installScript), 0o700); err != nil {
				t.Fatal(err)
			}
			callLog := filepath.Join(root, "calls.log")
			args := []string{"./install.sh", "--prefix", prefix, "--go", fakeGo, "--no-units"}
			if test.staged {
				args = append(args, "--destdir", filepath.Join(root, "stage"))
			}
			command := exec.Command("bash", args...)
			command.Env = append(os.Environ(), "PATH="+toolsDir+":"+os.Getenv("PATH"), "MIHOMOCTL_TEST_CALL_LOG="+callLog, "MIHOMOCTL_TEST_PREFIX="+prefix)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("install failed: %v\n%s", err, output)
			}
			logged, err := os.ReadFile(callLog)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			gotSync := strings.Contains(string(logged), "config sync-public-state")
			if gotSync != test.wantSync {
				t.Fatalf("sync invocation = %v, log=%q", gotSync, logged)
			}
		})
	}
}
