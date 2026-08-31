// Command picture-backend syncs photos from Nextcloud and serves them to the
// frame's frontend over a Unix socket.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/olivermaier/picture-frame/backend/internal/config"
	"github.com/olivermaier/picture-frame/backend/internal/frame"
	"github.com/olivermaier/picture-frame/backend/internal/ipcserver"
	"github.com/olivermaier/picture-frame/backend/internal/logging"
	"github.com/olivermaier/picture-frame/backend/internal/store"
)

func main() {
	configPath := flag.String("config", "/etc/picture-frame/config.toml", "path to config file")
	flag.Parse()

	log.SetFlags(log.Ltime)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	logging.SetVerbose(cfg.Verbose)

	// Print the settings that decide what the frame does, so a support question
	// can be answered from the log alone rather than by asking for the config.
	log.Printf("picture-backend starting (config %s)", *configPath)
	log.Printf("  server        %s", cfg.Server)
	log.Printf("  selection     %s", describeSelection(cfg))
	log.Printf("  sync interval %s", cfg.SyncInterval)
	log.Printf("  state dir     %s", cfg.StateDir)
	log.Printf("  socket        %s", cfg.SocketPath)
	log.Printf("  cache budget  %d MiB, source cap %d MP",
		cfg.CacheBudgetBytes>>20, cfg.MaxSourcePixels>>20)
	if cfg.Verbose {
		log.Print("  verbose       on")
	}

	for _, dir := range []string{cfg.StateDir, cfg.RenderDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("create %s: %v", dir, err)
		}
	}

	st, err := store.Open(cfg.ManifestPath(), cfg.CacheBudgetBytes)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	log.Printf("manifest %s holds %d photo(s)", cfg.ManifestPath(), st.Len())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	f := frame.New(cfg, st)

	// The sync worker runs independently of the socket: a frame with no network
	// keeps serving cached photos, and a frame with no frontend keeps syncing.
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()

	server := ipcserver.New(f, cfg.SocketPath)
	if err := server.Serve(ctx); err != nil {
		log.Printf("serve: %v", err)
	}

	<-done
	log.Print("shutting down; saving manifest")
	if err := st.Save(); err != nil {
		log.Printf("save manifest: %v", err)
	}
	log.Print("stopped")
}

// describeSelection renders the selection mode and its argument as one line.
func describeSelection(cfg config.Config) string {
	switch cfg.Selection {
	case config.SelectionFavorites:
		return "favorites"
	case config.SelectionFolder:
		return fmt.Sprintf("folder %q", cfg.Folder)
	default:
		return fmt.Sprintf("tag %q", cfg.Tag)
	}
}
