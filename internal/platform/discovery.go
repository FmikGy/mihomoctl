package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Installation describes the local Mihomo service without modifying it.
type Installation struct {
	BinaryPath string
	Unit       string
	UnitPath   string
	ExecArgs   []string
	ConfigDir  string
	ConfigPath string
}

type Discoverer struct {
	Runner         CommandRunner
	LookPath       func(string) (string, error)
	Stat           func(string) (os.FileInfo, error)
	UnitCandidates []string
	BinaryNames    []string
	BinaryPaths    []string
}

func NewDiscoverer(runner CommandRunner) *Discoverer {
	return &Discoverer{
		Runner:         runner,
		LookPath:       exec.LookPath,
		Stat:           os.Stat,
		UnitCandidates: []string{"mihomo.service"},
		BinaryNames:    []string{"mihomo"},
		BinaryPaths:    []string{"/usr/bin/mihomo", "/usr/local/bin/mihomo"},
	}
}

func (d *Discoverer) Discover(ctx context.Context) (Installation, error) {
	if d == nil || d.Runner == nil {
		return Installation{}, fmt.Errorf("discovery command runner is required")
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.Stat == nil {
		d.Stat = os.Stat
	}
	if len(d.UnitCandidates) == 0 {
		d.UnitCandidates = []string{"mihomo.service"}
	}

	installation, unitErr := d.discoverUnit(ctx)
	installation.BinaryPath = d.findBinary(installation)
	if installation.BinaryPath == "" {
		return installation, fmt.Errorf("mihomo executable was not found")
	}
	if unitErr != nil {
		return installation, unitErr
	}
	return installation, nil
}

func (d *Discoverer) discoverUnit(ctx context.Context) (Installation, error) {
	for _, unit := range d.UnitCandidates {
		if !unitNamePattern.MatchString(unit) {
			continue
		}
		result, err := d.Runner.Run(ctx, "systemctl", "show", "--no-pager",
			"--property=LoadState", "--property=FragmentPath", "--property=ExecStart", "--", unit)
		if err != nil {
			continue
		}
		properties := parseProperties(string(result.Stdout))
		if properties["LoadState"] == "" || properties["LoadState"] == "not-found" {
			continue
		}

		path, args, err := parseSystemdExecStart(properties["ExecStart"])
		if err != nil {
			return Installation{}, fmt.Errorf("parse %s ExecStart: %w", unit, err)
		}
		configDir, configPath := extractConfigPaths(args)
		return Installation{
			BinaryPath: path,
			Unit:       unit,
			UnitPath:   properties["FragmentPath"],
			ExecArgs:   args,
			ConfigDir:  configDir,
			ConfigPath: configPath,
		}, nil
	}
	return Installation{}, fmt.Errorf("mihomo systemd service was not found")
}

func (d *Discoverer) findBinary(installation Installation) string {
	if installation.BinaryPath != "" {
		if info, err := d.Stat(installation.BinaryPath); err == nil && isExecutable(info) {
			return installation.BinaryPath
		}
	}
	for _, name := range d.BinaryNames {
		if path, err := d.LookPath(name); err == nil {
			return path
		}
	}
	for _, path := range d.BinaryPaths {
		if info, err := d.Stat(path); err == nil && isExecutable(info) {
			return path
		}
	}
	return ""
}

func isExecutable(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func parseSystemdExecStart(value string) (string, []string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, fmt.Errorf("ExecStart is empty")
	}

	path := extractSystemdField(value, "path")
	if path != "" {
		parsedPath, err := splitCommandLine(path)
		if err != nil || len(parsedPath) != 1 {
			return "", nil, fmt.Errorf("invalid ExecStart path")
		}
		path = parsedPath[0]
	}
	argvText := extractSystemdField(value, "argv[]")
	if argvText == "" {
		argvText = value
	}
	args, err := splitCommandLine(argvText)
	if err != nil {
		return "", nil, err
	}
	if len(args) == 0 {
		return "", nil, fmt.Errorf("ExecStart has no arguments")
	}
	if path == "" {
		path = args[0]
	}
	if path == "" {
		return "", nil, fmt.Errorf("ExecStart executable is empty")
	}
	return path, args, nil
}

func extractSystemdField(value, name string) string {
	marker := name + "="
	start := strings.Index(value, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	rest := value[start:]
	if end := strings.Index(rest, " ; "); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(strings.Trim(rest, "{}"))
}

func splitCommandLine(value string) ([]string, error) {
	var args []string
	var token strings.Builder
	var quote byte
	tokenStarted := false
	flush := func() {
		if tokenStarted {
			args = append(args, token.String())
			token.Reset()
			tokenStarted = false
		}
	}

	for i := 0; i < len(value); i++ {
		char := value[i]
		if char == '\\' {
			tokenStarted = true
			i++
			if i >= len(value) {
				return nil, fmt.Errorf("unterminated escape in ExecStart")
			}
			switch value[i] {
			case 'n':
				token.WriteByte('\n')
			case 'r':
				token.WriteByte('\r')
			case 't':
				token.WriteByte('\t')
			case 's':
				token.WriteByte(' ')
			case 'x':
				if i+2 >= len(value) {
					return nil, fmt.Errorf("invalid hex escape in ExecStart")
				}
				decoded, err := strconv.ParseUint(value[i+1:i+3], 16, 8)
				if err != nil {
					return nil, fmt.Errorf("invalid hex escape in ExecStart")
				}
				token.WriteByte(byte(decoded))
				i += 2
			default:
				token.WriteByte(value[i])
			}
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				token.WriteByte(char)
			}
			tokenStarted = true
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
			tokenStarted = true
		case ' ', '\t', '\n':
			flush()
		default:
			token.WriteByte(char)
			tokenStarted = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoting in ExecStart")
	}
	flush()
	return args, nil
}

func extractConfigPaths(args []string) (configDir, configPath string) {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-d":
			if i+1 < len(args) {
				configDir = args[i+1]
				i++
			}
		case "-f":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(arg, "-d=") {
				configDir = strings.TrimPrefix(arg, "-d=")
			} else if strings.HasPrefix(arg, "-f=") {
				configPath = strings.TrimPrefix(arg, "-f=")
			}
		}
	}
	if configPath == "" && configDir != "" {
		configPath = filepath.Join(configDir, "config.yaml")
	} else if configPath != "" && !filepath.IsAbs(configPath) && configDir != "" {
		configPath = filepath.Join(configDir, configPath)
	}
	configDir = filepath.Clean(configDir)
	configPath = filepath.Clean(configPath)
	if configDir == "." {
		configDir = ""
	}
	if configPath == "." {
		configPath = ""
	}
	return configDir, configPath
}
