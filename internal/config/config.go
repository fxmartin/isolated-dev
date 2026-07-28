package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

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

type Command struct {
	Args    []string `toml:"args"`
	Workdir string   `toml:"workdir"`
	Compose bool     `toml:"compose"`
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
	}

	for name, command := range cfg.Commands {
		if strings.TrimSpace(name) == "" || len(command.Args) == 0 {
			return fmt.Errorf("commands.%s.args: must not be empty", name)
		}
	}
	return nil
}

func validPort(value int) bool {
	return value > 0 && value <= 65535
}
