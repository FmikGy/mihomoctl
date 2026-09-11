package scripts_test

import (
	"os/exec"
	"testing"
)

func TestValidateReleaseVersion(t *testing.T) {
	for _, version := range []string{"v0.1.0", "v1.20.300", "v10.0.2"} {
		if output, err := exec.Command("bash", "./validate-release-version.sh", version).CombinedOutput(); err != nil {
			t.Errorf("valid version %q was rejected: %v\n%s", version, err, output)
		}
	}
	for _, version := range []string{"", "1.2.3", "v01.2.3", "v1.02.3", "v1.2.03", "v1.2.3-rc.1", "v1.2.3+build", "v1.2", "v1.2.3.4"} {
		if err := exec.Command("bash", "./validate-release-version.sh", version).Run(); err == nil {
			t.Errorf("invalid version %q was accepted", version)
		}
	}
}
