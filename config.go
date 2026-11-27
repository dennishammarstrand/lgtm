package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2/styles"
	"go.yaml.in/yaml/v3"
)

// config holds the defaults for the command-line flags; flags override it.
type config struct {
	Notifications bool          `yaml:"notifications"`
	Interval      time.Duration `yaml:"interval"`
	Query         string        `yaml:"query"`
	Mine          bool          `yaml:"mine"`
	Theme         string        `yaml:"theme"`
}

func defaultConfig() config {
	return config{Notifications: true, Interval: time.Minute, Query: defaultQuery, Mine: true, Theme: "catppuccin-mocha"}
}

// configPath is $LGTM_CONFIG, else config.yml in $XDG_CONFIG_HOME/lgtm,
// ~/.config/lgtm or, on Windows, %APPDATA%\lgtm.
func configPath() string {
	if p := os.Getenv("LGTM_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" && runtime.GOOS == "windows" {
		dir, _ = os.UserConfigDir()
	}
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "lgtm", "config.yml")
}

// loadConfig reads the config file; settings it leaves out keep their
// defaults, and a missing file means all defaults. Unknown keys are an error,
// so a typo doesn't silently do nothing.
func loadConfig(path string) (config, error) {
	c := defaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return defaultConfig(), fmt.Errorf("%s: %w", path, err)
	}
	if c.Interval <= 0 {
		return defaultConfig(), fmt.Errorf("%s: interval must be positive, e.g. 2m", path)
	}
	return c, nil
}

// checkTheme reports an unknown syntax theme, listing the valid ones; chroma
// itself would silently fall back to another theme.
func checkTheme(name string) error {
	if _, ok := styles.Registry[name]; ok {
		return nil
	}
	return fmt.Errorf("unknown theme %q, choose one of:\n  %s\nPreviews: https://xyproto.github.io/splash/docs/", name, strings.Join(styles.Names(), ", "))
}
