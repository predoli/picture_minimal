// Package ipcserver speaks the newline-delimited JSON protocol the frontend
// uses over a Unix socket.
//
// One short-lived connection per request. That keeps both ends trivially
// recoverable: either process can restart without the other noticing anything
// worse than a single failed exchange.
package ipcserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/olivermaier/picture-frame/backend/internal/frame"
	"github.com/olivermaier/picture-frame/backend/internal/logging"
)

// Request is what the frontend sends. It declares its own geometry, so the
// backend always renders to the true panel size rather than a configured guess.
type Request struct {
	Width  int    `json:"w"`
	Height int    `json:"h"`
	Format string `json:"format"`
	Last   string `json:"last"`
}

// Response covers all three shapes the frontend understands.
type Response struct {
	Mode string `json:"mode,omitempty"` // "pairing", "empty", or absent for an image

	// Image
	ID     string `json:"id,omitempty"`
	Blob   string `json:"blob,omitempty"`
	Width  int    `json:"w,omitempty"`
	Height int    `json:"h,omitempty"`
	Stride int    `json:"stride,omitempty"`
	Name   string `json:"name,omitempty"`

	// Pairing
	URL  string `json:"url,omitempty"`
	Host string `json:"host,omitempty"`

	// Empty
	Message string `json:"message,omitempty"`

	Error string `json:"error,omitempty"`
}

type Server struct {
	frame *frame.Frame
	path  string
}

func New(f *frame.Frame, socketPath string) *Server {
	return &Server{frame: f, path: socketPath}
}

// Serve listens until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	// A socket left behind by an unclean shutdown would make Listen fail with
	// "address already in use" even though nothing is holding it.
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}

	listener, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(s.path)

	// 0660 with the frontend in the same group: the two processes run as
	// different concerns but the same user, and nothing else needs access.
	if err := os.Chmod(s.path, 0o660); err != nil {
		log.Printf("chmod socket: %v", err)
	}

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	log.Printf("listening on %s", s.path)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("accept: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	// A client that connects and then goes quiet must not pin a goroutine.
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		log.Printf("malformed request %q: %v", line, err)
		writeJSON(conn, Response{Error: "malformed request"})
		return
	}
	if req.Format != "" && req.Format != "rgb565" {
		log.Printf("rejecting request for unsupported format %q", req.Format)
		writeJSON(conn, Response{Error: "unsupported format " + req.Format})
		return
	}

	resp := toResponse(s.frame.Next(req.Width, req.Height, req.Last))
	switch {
	case resp.Mode != "":
		logging.Debugf("replied %s: %s%s", resp.Mode, resp.Message, resp.URL)
	default:
		logging.Debugf("replied with %s (%dx%d, stride %d)", resp.Name, resp.Width, resp.Height, resp.Stride)
	}
	writeJSON(conn, resp)
}

func toResponse(result frame.Result) Response {
	switch result.Kind {
	case frame.ResultPairing:
		return Response{Mode: "pairing", URL: result.LoginURL, Host: result.Host}

	case frame.ResultEmpty:
		return Response{Mode: "empty", Message: result.Message}

	default:
		e := result.Entry
		return Response{
			ID:     e.ID(),
			Blob:   e.Blob,
			Width:  e.Width,
			Height: e.Height,
			Stride: e.Stride,
			Name:   e.Name,
		}
	}
}

func writeJSON(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	conn.Write(append(data, '\n'))
}
