package ipcserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivermaier/picture-frame/backend/internal/config"
	"github.com/olivermaier/picture-frame/backend/internal/frame"
	"github.com/olivermaier/picture-frame/backend/internal/store"
)

// exchange performs one request/response round trip, exactly as the C++ client
// does: connect, write one NDJSON line, read one back, close.
func exchange(t *testing.T, socket string, req Request) Response {
	t.Helper()

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return resp
}

func startServer(t *testing.T) string {
	t.Helper()

	// Unix socket paths cap at ~104 bytes. t.TempDir() on macOS produces a path
	// long enough (it embeds the test name) to blow that limit on its own, so
	// the socket gets its own short directory.
	socketDir, err := os.MkdirTemp("/tmp", "pf")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "s.sock")

	cfg := config.Default()
	cfg.Server = "https://cloud.example.invalid"
	cfg.StateDir = t.TempDir()
	cfg.SocketPath = socket

	st, err := store.Open(cfg.ManifestPath(), cfg.CacheBudgetBytes)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	server := New(frame.New(cfg, st), socket)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ctx) }()

	// Wait for the listener rather than sleeping a fixed amount, and surface a
	// startup failure instead of timing out with no explanation.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-serveErr:
			t.Fatalf("server exited during startup: %v", err)
		default:
		}
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			return socket
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start listening")
	return ""
}

func TestUnpairedFrameAnswersWithoutBlocking(t *testing.T) {
	socket := startServer(t)

	// An unpaired frame with an unreachable server must still answer promptly:
	// the frontend has a five second timeout and a blank wall is the failure
	// mode we are avoiding.
	resp := exchange(t, socket, Request{Width: 1920, Height: 1080, Format: "rgb565"})

	if resp.Mode != "pairing" && resp.Mode != "empty" {
		t.Fatalf("mode = %q, want pairing or empty; got %+v", resp.Mode, resp)
	}
	if resp.Mode == "empty" && resp.Message == "" {
		t.Error("empty replies must carry a message for the frame to display")
	}
	if resp.Error != "" {
		t.Errorf("unexpected error field: %q", resp.Error)
	}
}

func TestRejectsUnsupportedPixelFormat(t *testing.T) {
	socket := startServer(t)

	// The blob layout is fixed by the frontend's LV_COLOR_DEPTH. Silently
	// serving RGB565 to a client asking for something else would render garbage.
	resp := exchange(t, socket, Request{Width: 800, Height: 480, Format: "rgb888"})

	if resp.Error == "" {
		t.Fatalf("expected an error for an unsupported format, got %+v", resp)
	}
}

func TestMalformedRequestGetsAnErrorNotASilentDrop(t *testing.T) {
	socket := startServer(t)

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == "" {
		t.Error("malformed requests should be answered with an error, not ignored")
	}
}

func TestImageResultSerialisesTheFieldsTheFrontendNeeds(t *testing.T) {
	entry := store.Entry{
		Href:   "/dav/a.jpg",
		Name:   "a.jpg",
		ETag:   "abc",
		Blob:   "/var/lib/picture-frame/render/abc.rgb565",
		Width:  1920,
		Height: 1080,
		Stride: 3840,
	}

	resp := toResponse(frame.Result{Kind: frame.ResultImage, Entry: entry})

	if resp.Mode != "" {
		t.Errorf("image replies carry no mode, got %q", resp.Mode)
	}
	if resp.Stride != 1920*2 {
		t.Errorf("stride = %d, want %d (packed RGB565 rows)", resp.Stride, 1920*2)
	}
	if resp.ID != entry.ID() {
		t.Errorf("id = %q, want %q; the frontend echoes this back as \"last\"", resp.ID, entry.ID())
	}
	if resp.Blob == "" || resp.Width == 0 || resp.Height == 0 {
		t.Errorf("incomplete image reply: %+v", resp)
	}
}
