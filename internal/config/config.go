package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// commandNamePattern keeps a declared command name usable as a single
// `isolated-dev run` argument, so invoking one never depends on quoting.
var commandNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// packageNamePattern admits exactly what Debian admits for a package name:
// lower-case alphanumerics plus `+ - .`, at least two characters, starting
// alphanumeric. The names reach a root shell inside the guest, so nothing
// looser gets past the configuration.
var packageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

const (
	SharedFileName     = ".isolated-dev.toml"
	LocalFileName      = ".isolated-dev.local.toml"
	DefaultBaseImage   = "local/isolated-dev-base:1"
	DefaultMountTarget = "/workspace"
)

type Config struct {
	Version     int                `toml:"version"`
	BaseImage   string             `toml:"base_image"`
	MountTarget string             `toml:"mount_target"`
	Packages    []string           `toml:"packages"`
	Bootstrap   []string           `toml:"bootstrap"`
	Resources   Resources          `toml:"resources"`
	Ports       []Port             `toml:"ports"`
	Commands    map[string]Command `toml:"commands"`
	Secrets     SecretReferences   `toml:"secrets"`
}

type Resources struct {
	CPUs     int `toml:"cpus"`
	MemoryGB int `toml:"memory_gb"`
}

type Port struct {
	Name  string `toml:"name"`
	Guest int    `toml:"guest"`
	Host  int    `toml:"host"`
}

// Command is a project command that exists only because configuration declares
// it. Nothing in the repository — a Compose file, a task runner manifest, a
// script directory — ever becomes a command on its own, and a declared command
// runs only when it is invoked by name.
type Command struct {
	Args []string `toml:"args"`
	// Workdir is an optional project-relative directory the command runs in.
	Workdir string `toml:"workdir"`
	// Compose marks a command that needs the guest Docker daemon, so readiness
	// is confirmed before it runs.
	Compose bool `toml:"compose"`
}

type SecretReferences struct {
	Environment []string `toml:"environment"`
	Files       []string `toml:"files"`
}

type localConfig struct {
	Resources localResources `toml:"resources"`
	Ports     []localPort    `toml:"ports"`
}

type localResources struct {
	CPUs     *int `toml:"cpus"`
	MemoryGB *int `toml:"memory_gb"`
}

type localPort struct {
	Name string `toml:"name"`
	Host int    `toml:"host"`
}

func Defaults() Config {
	return Config{
		Version:     1,
		BaseImage:   DefaultBaseImage,
		MountTarget: DefaultMountTarget,
		Resources: Resources{
			CPUs:     4,
			MemoryGB: 8,
		},
		Commands: make(map[string]Command),
	}
}

func Load(projectDir string) (Config, error) {
	cfg := Defaults()
	if err := decodeOptional(filepath.Join(projectDir, SharedFileName), &cfg); err != nil {
		return Config{}, err
	}

	var local localConfig
	localFound, err := decodeOptionalFile(filepath.Join(projectDir, LocalFileName), &local)
	if err != nil {
		return Config{}, err
	}
	if localFound {
		if err := mergeLocal(&cfg, local); err != nil {
			return Config{}, fmt.Errorf("%s: %w", LocalFileName, err)
		}
	}

	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("effective configuration: %w", err)
	}
	return cfg, nil
}

func decodeOptional(path string, target any) error {
	_, err := decodeOptionalFile(path, target)
	return err
}

func decodeOptionalFile(path string, target any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: read configuration: %w", filepath.Base(path), err)
	}

	metadata, err := toml.Decode(string(data), target)
	if err != nil {
		return false, fmt.Errorf("%s: invalid TOML", filepath.Base(path))
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		fields := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			fields = append(fields, key.String())
		}
		sort.Strings(fields)
		return false, fmt.Errorf(
			"%s: unsupported field %s",
			filepath.Base(path),
			strings.Join(fields, ", "),
		)
	}
	return true, nil
}

func mergeLocal(cfg *Config, local localConfig) error {
	if local.Resources.CPUs != nil {
		cfg.Resources.CPUs = *local.Resources.CPUs
	}
	if local.Resources.MemoryGB != nil {
		cfg.Resources.MemoryGB = *local.Resources.MemoryGB
	}
	portsByName := make(map[string]int, len(cfg.Ports))
	for index, port := range cfg.Ports {
		portsByName[port.Name] = index
	}
	for _, override := range local.Ports {
		index, ok := portsByName[override.Name]
		if !ok {
			return fmt.Errorf("ports.%s: no matching shared port", override.Name)
		}
		cfg.Ports[index].Host = override.Host
	}
	return nil
}

func validate(cfg Config) error {
	if cfg.Version != 1 {
		return fmt.Errorf("version: unsupported value %d", cfg.Version)
	}
	if strings.TrimSpace(cfg.BaseImage) == "" {
		return errors.New("base_image: must not be empty")
	}
	if !filepath.IsAbs(cfg.MountTarget) {
		return errors.New("mount_target: must be an absolute Linux path")
	}
	if cfg.Resources.CPUs <= 0 {
		return errors.New("resources.cpus: must be positive")
	}
	if cfg.Resources.MemoryGB <= 0 {
		return errors.New("resources.memory_gb: must be positive")
	}
	names := make(map[string]struct{}, len(cfg.Ports))
	hostPorts := make(map[int]string, len(cfg.Ports))
	for index, port := range cfg.Ports {
		field := fmt.Sprintf("ports[%d]", index)
		if strings.TrimSpace(port.Name) == "" {
			return fmt.Errorf("%s.name: must not be empty", field)
		}
		if _, exists := names[port.Name]; exists {
			return fmt.Errorf("%s.name: duplicate %q", field, port.Name)
		}
		names[port.Name] = struct{}{}
		if !validPort(port.Guest) {
			return fmt.Errorf("%s.guest: must be between 1 and 65535", field)
		}
		if !validPort(port.Host) {
			return fmt.Errorf("%s.host: must be between 1 and 65535", field)
		}
		// One macOS port can carry one forward, so a clash — including one a
		// local override introduces — is rejected before any tunnel is built.
		if owner, exists := hostPorts[port.Host]; exists {
			return fmt.Errorf("%s.host: %d is already forwarded by ports.%s", field, port.Host, owner)
		}
		hostPorts[port.Host] = port.Name
	}

	for index, name := range cfg.Packages {
		if !packageNamePattern.MatchString(name) {
			return fmt.Errorf("packages[%d]: invalid Ubuntu package name %q", index, name)
		}
	}

	for _, name := range cfg.CommandNames() {
		if err := validateCommand(name, cfg.Commands[name]); err != nil {
			return err
		}
	}
	return validateSecrets(cfg.Secrets)
}

// CommandNames lists the declared command names in a stable order, so
// diagnostics that enumerate them read the same way on every run.
func (cfg Config) CommandNames() []string {
	names := make([]string, 0, len(cfg.Commands))
	for name := range cfg.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateCommand(name string, command Command) error {
	if !commandNamePattern.MatchString(name) {
		return fmt.Errorf(
			"commands.%q: must be a name usable as an `isolated-dev run` argument",
			name,
		)
	}
	if len(command.Args) == 0 {
		return fmt.Errorf("commands.%s.args: must not be empty", name)
	}
	program := strings.TrimSpace(command.Args[0])
	// The first argument names the program to run. An empty one, or one shaped
	// like an environment assignment, would change what the guest executes
	// rather than which program it executes.
	if program == "" || strings.Contains(program, "=") {
		return fmt.Errorf(
			"commands.%s.args[0]: must be the program to run, not an environment assignment",
			name,
		)
	}
	return validateCommandWorkdir(name, command.Workdir)
}

func validateCommandWorkdir(name string, workdir string) error {
	if workdir == "" {
		return nil
	}
	if filepath.IsAbs(workdir) {
		return fmt.Errorf("commands.%s.workdir: must be a project-relative path", name)
	}
	cleaned := filepath.Clean(workdir)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("commands.%s.workdir: must stay inside the project", name)
	}
	return nil
}

// validateSecrets keeps secret references pointing inside the mounted project
// and keeps inline values out. Its errors never echo the offending value,
// because a rejected entry may itself be a secret.
func validateSecrets(secrets SecretReferences) error {
	for index, path := range secrets.Files {
		field := fmt.Sprintf("secrets.files[%d]", index)
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("%s: must not be empty", field)
		}
		if filepath.IsAbs(path) {
			return fmt.Errorf("%s: must be a project-relative path", field)
		}
		cleaned := filepath.Clean(path)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s: must stay inside the project", field)
		}
	}
	for index, name := range secrets.Environment {
		if !environmentNamePattern.MatchString(name) {
			return fmt.Errorf(
				"secrets.environment[%d]: must be an environment variable name, not a value",
				index,
			)
		}
	}
	return nil
}

func validPort(value int) bool {
	return value > 0 && value <= 65535
}
