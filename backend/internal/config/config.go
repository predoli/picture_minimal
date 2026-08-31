// Package config loads the frame's non-secret settings.
//
// Secrets never appear here. The Nextcloud credential is obtained by the frame
// itself through QR pairing and stored separately (see internal/auth); the
// Tailscale key is only ever used at install time.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Selection decides which of the account's files reach the frame.
type Selection string

const (
	// SelectionTag filters on a Nextcloud collaborative tag. Recommended: the
	// user curates the frame from their phone, and untagging removes a photo.
	SelectionTag Selection = "tag"
	// SelectionFavorites filters on the per-user favourite flag.
	SelectionFavorites Selection = "favorites"
	// SelectionFolder walks a single directory. The always-works fallback.
	SelectionFolder Selection = "folder"
)

type Config struct {
	// Server is the only thing that must be known before pairing, which is why
	// it is non-secret configuration rather than something the QR flow supplies.
	Server string `toml:"server"`

	Selection Selection `toml:"selection"`
	Tag       string    `toml:"tag"`
	Folder    string    `toml:"folder"`

	SyncInterval    time.Duration `toml:"-"`
	SyncIntervalRaw string        `toml:"sync_interval"`

	SocketPath string `toml:"socket_path"`
	StateDir   string `toml:"state_dir"`

	// MaxSourcePixels caps decode size. A 12 MP frame is ~36 MB decoded before
	// any decoder overhead, on a board with 512 MB of RAM.
	MaxSourcePixels int64 `toml:"max_source_pixels"`

	// CacheBudgetBytes bounds originals plus rendered blobs so the SD card
	// cannot fill.
	CacheBudgetBytes int64 `toml:"cache_budget_bytes"`

	// Hostname distinguishes frames on the pairing screen and names the app
	// password in the user's Nextcloud security settings.
	Hostname string `toml:"hostname"`

	// Verbose adds per-request and per-HTTP-call detail to the log. Also
	// settable with FRAME_VERBOSE=1, which needs no config edit and no restart
	// of anything but the service itself.
	Verbose bool `toml:"verbose"`
}

func Default() Config {
	host, _ := os.Hostname()
	if host == "" {
		host = "picture-frame"
	}
	return Config{
		Selection:        SelectionTag,
		Tag:              "Frame",
		Folder:           "/Photos/PictureFrame",
		SyncInterval:     15 * time.Minute,
		SocketPath:       "/run/picture-frame/frontend.sock",
		StateDir:         "/var/lib/picture-frame",
		MaxSourcePixels:  40 << 20, // 40 MP
		CacheBudgetBytes: 2 << 30,  // 2 GiB
		Hostname:         host,
		Verbose:          os.Getenv("FRAME_VERBOSE") == "1",
	}
}

// Load reads path, falling back to defaults for anything absent. A missing file
// is not an error: a freshly imaged frame should still boot far enough to show
// the pairing screen and say what is wrong.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}

	if os.Getenv("FRAME_VERBOSE") == "1" {
		cfg.Verbose = true
	}

	if cfg.SyncIntervalRaw != "" {
		d, err := time.ParseDuration(cfg.SyncIntervalRaw)
		if err != nil {
			return cfg, fmt.Errorf("sync_interval %q: %w", cfg.SyncIntervalRaw, err)
		}
		cfg.SyncInterval = d
	}

	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server is required (e.g. server = \"https://cloud.example.com\")")
	}
	if !strings.HasPrefix(c.Server, "https://") && !strings.HasPrefix(c.Server, "http://") {
		return fmt.Errorf("server %q must include a scheme", c.Server)
	}
	switch c.Selection {
	case SelectionTag:
		if c.Tag == "" {
			return fmt.Errorf("selection = \"tag\" requires a tag name")
		}
	case SelectionFavorites:
	case SelectionFolder:
		if c.Folder == "" {
			return fmt.Errorf("selection = \"folder\" requires a folder path")
		}
	default:
		return fmt.Errorf("selection %q must be tag, favorites or folder", c.Selection)
	}
	if c.SyncInterval <= 0 {
		return fmt.Errorf("sync_interval must be positive")
	}
	return nil
}

func (c Config) AuthPath() string  { return filepath.Join(c.StateDir, "auth.json") }
func (c Config) CacheDir() string  { return filepath.Join(c.StateDir, "cache") }
func (c Config) RenderDir() string { return filepath.Join(c.StateDir, "render") }
func (c Config) ManifestPath() string {
	return filepath.Join(c.StateDir, "manifest.json")
}
