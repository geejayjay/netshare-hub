// Package config loads the bootstrap settings: the few values the server needs
// before it can open its database. Everything else is a database-backed
// setting edited in the UI, so a setting never has two sources.
//
// Order: built-in defaults < TOML file < NSH_* environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Listen is the address the web server binds to.
	Listen string `toml:"listen"`
	// DataDir holds the database and other state.
	DataDir string `toml:"data_dir"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `toml:"log_level"`
	// LogFormat is json (default) or text (nicer in a dev terminal).
	LogFormat string `toml:"log_format"`
	// SecureCookies marks the session cookie HTTPS-only. Turn it off only for
	// local development over plain http.
	SecureCookies bool `toml:"secure_cookies"`
}

func Default() Config {
	return Config{
		Listen:        "127.0.0.1:8080",
		DataDir:       "./data",
		LogLevel:      "info",
		LogFormat:     "json",
		SecureCookies: true,
	}
}

// Load reads the config. An empty path means "no file"; a path that does not
// exist is an error, because a typo should not silently fall back to defaults.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return cfg, fmt.Errorf("config file %q not found", path)
			}
			return cfg, fmt.Errorf("read config %q: %w", path, err)
		}
	}

	env(&cfg.Listen, "NSH_LISTEN")
	env(&cfg.DataDir, "NSH_DATA_DIR")
	env(&cfg.LogLevel, "NSH_LOG_LEVEL")
	env(&cfg.LogFormat, "NSH_LOG_FORMAT")
	if v, ok := os.LookupEnv("NSH_SECURE_COOKIES"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("NSH_SECURE_COOKIES must be true or false, got %q", v)
		}
		cfg.SecureCookies = b
	}
	return cfg, nil
}

func env(dst *string, name string) {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		*dst = v
	}
}
