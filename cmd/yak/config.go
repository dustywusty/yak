package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dustywusty/yak/sdk/go/yak"
)

// Profile is one saved endpoint + credential. Tokens are stored in the
// config file with 0600 permissions; YAK_TOKEN overrides it.
type Profile struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
	Board string `json:"board,omitempty"`
}

type Config struct {
	Current  string             `json:"current"`
	Profiles map[string]Profile `json:"profiles"`
}

var gf struct {
	profile   string
	url       string
	board     string
	tokenFile string
	json      bool
	runID     string
}

func addGlobalFlags(root *cobra.Command) {
	pf := root.PersistentFlags()
	pf.StringVar(&gf.profile, "profile", "", "config profile (env YAK_PROFILE)")
	pf.StringVar(&gf.url, "url", "", "server base URL (env YAK_URL)")
	pf.StringVar(&gf.board, "board", "", "board ID (env YAK_BOARD)")
	pf.StringVar(&gf.tokenFile, "token-file", "", "read the bearer token from this file (env YAK_TOKEN_FILE; YAK_TOKEN holds a token)")
	pf.BoolVar(&gf.json, "json", false, "print JSON (NDJSON for streams)")
	pf.StringVar(&gf.runID, "run-id", "", "attach Yak-Run-Id to requests (env YAK_RUN_ID); attribution only")
}

func configPath() string {
	if p := yakEnv("YAK_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = xdg
	} else if home, err := os.UserHomeDir(); err == nil {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "yak", "config.json")
}

func loadConfig() (*Config, error) {
	c := &Config{Profiles: map[string]Profile{}}
	path := configPath()
	b, err := os.ReadFile(path)
	// Keep existing logins readable; future saves use the new directory.
	if errors.Is(err, os.ErrNotExist) && yakEnv("YAK_CONFIG") == "" {
		oldPath := filepath.Join(filepath.Dir(filepath.Dir(path)), "abby", "config.json")
		b, err = os.ReadFile(oldPath)
	}
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath(), err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	return c, nil
}

func saveConfig(c *Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func profileName(c *Config) string {
	if gf.profile != "" {
		return gf.profile
	}
	if p := yakEnv("YAK_PROFILE"); p != "" {
		return p
	}
	if c.Current != "" {
		return c.Current
	}
	return "default"
}

// resolved returns the effective profile after flags and environment.
func resolved() (Profile, error) {
	c, err := loadConfig()
	if err != nil {
		return Profile{}, err
	}
	p := c.Profiles[profileName(c)]
	if v := yakEnv("YAK_URL"); v != "" {
		p.URL = v
	}
	if gf.url != "" {
		p.URL = gf.url
	}
	if v := yakEnv("YAK_TOKEN"); v != "" {
		p.Token = v
	}
	// Select the credential environment as a family: a new token must not
	// be overridden by a stale legacy token-file setting.
	tf := yakEnv("YAK_TOKEN_FILE")
	if os.Getenv("YAK_TOKEN") != "" && os.Getenv("YAK_TOKEN_FILE") == "" {
		tf = ""
	}
	if gf.tokenFile != "" {
		tf = gf.tokenFile
	}
	if tf != "" {
		b, err := os.ReadFile(tf)
		if err != nil {
			return p, err
		}
		p.Token = strings.TrimSpace(string(b))
	}
	if v := yakEnv("YAK_BOARD"); v != "" {
		p.Board = v
	}
	if gf.board != "" {
		p.Board = gf.board
	}
	if p.URL == "" {
		p.URL = "http://localhost:8080"
	}
	return p, nil
}

func client() (*yak.Client, Profile, error) {
	p, err := resolved()
	if err != nil {
		return nil, p, err
	}
	c := yak.New(p.URL, p.Token)
	c.UserAgent = "yak-cli/" + version
	c.RunID = gf.runID
	if c.RunID == "" {
		c.RunID = yakEnv("YAK_RUN_ID")
	}
	return c, p, nil
}

func boardID(p Profile) (string, error) {
	if p.Board == "" {
		return "", usage("no board selected; pass --board, set YAK_BOARD, or run `yak board use <id>`")
	}
	return p.Board, nil
}

func readSecret(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	return strings.TrimSpace(string(b)), err
}
