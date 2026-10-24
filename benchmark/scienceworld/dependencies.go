package scienceworld

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type DependencyStatus struct {
	Python3         bool   `json:"python3"`
	ScienceWorld    bool   `json:"scienceworld_python_package"`
	Java            bool   `json:"java"`
	Prlimit         bool   `json:"prlimit"`
	ScienceWorldErr string `json:"scienceworld_error,omitempty"`
}

// CheckDependencies inspects local executables and imports without network or
// model-provider access. It does not install packages or start the simulator.
func CheckDependencies(ctx context.Context) DependencyStatus {
	status := DependencyStatus{}
	python, err := exec.LookPath("python3")
	if err == nil {
		version, versionErr := exec.CommandContext(ctx, python, "--version").CombinedOutput()
		status.Python3 = versionErr == nil && pythonAtLeast38(string(version))
		if status.Python3 {
			importCmd := exec.CommandContext(ctx, python, "-I", "-c", "import scienceworld")
			if err := importCmd.Run(); err == nil {
				status.ScienceWorld = true
			} else {
				status.ScienceWorldErr = "Python package 'scienceworld' is not importable in the active Python 3 environment"
			}
		}
	}
	java, err := exec.LookPath("java")
	if err == nil {
		status.Java = exec.CommandContext(ctx, java, "-version").Run() == nil
	}
	_, err = exec.LookPath("prlimit")
	status.Prlimit = err == nil
	return status
}

func (s DependencyStatus) Ready() error {
	missing := []string{}
	if !s.Python3 {
		missing = append(missing, "Python 3.8+")
	}
	if !s.ScienceWorld {
		missing = append(missing, "the official scienceworld Python package")
	}
	if !s.Java {
		missing = append(missing, "Java 8+")
	}
	if len(missing) > 0 {
		return fmt.Errorf("ScienceWorld integration unavailable: missing %s", strings.Join(missing, ", "))
	}
	return nil
}

func pythonAtLeast38(version string) bool {
	version = strings.TrimSpace(strings.TrimPrefix(version, "Python"))
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := parsePositive(parts[0])
	minor, minorErr := parsePositive(parts[1])
	return majorErr == nil && minorErr == nil && (major > 3 || (major == 3 && minor >= 8))
}

func parsePositive(value string) (int, error) {
	var number int
	_, err := fmt.Sscanf(value, "%d", &number)
	if err != nil {
		return 0, err
	}
	return number, nil
}
