package nextcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FlowLifetime is how long a poll token stays valid server-side. The frame must
// restart the flow before this elapses, or the QR on the wall becomes a code
// that cannot be redeemed.
const FlowLifetime = 20 * time.Minute

// ErrFlowPending means the user has not finished approving yet. Expected, and
// returned on every poll until they do.
var ErrFlowPending = errors.New("nextcloud: login flow not yet approved")

// ErrFlowExpired means the poll token is no longer known to the server.
var ErrFlowExpired = errors.New("nextcloud: login flow expired")

// newFlowClient returns a client that remembers where it was redirected.
//
// Login Flow v2 is POST-only, and Go follows a 301/302 by reissuing the request
// as a GET, which Nextcloud answers with 405. The usual cause is an http:// URL
// on a server that redirects to https://, and the bare status code gives no hint
// of that, so record the target and put it in the error.
func newFlowClient(redirectedTo *string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			*redirectedTo = req.URL.String()
			return nil
		},
	}
}

// redirectHint explains a failure that a redirect probably caused.
func redirectHint(requested, redirected string) string {
	if redirected == "" {
		return ""
	}
	return fmt.Sprintf(" (the server redirected %s to %s; a redirect turns the required POST"+
		" into a GET, which Nextcloud rejects - set the redirect target as `server` in"+
		" config.toml)", requested, redirected)
}

// Flow is an in-progress Login Flow v2 pairing.
type Flow struct {
	// LoginURL is what the frame renders as a QR code.
	LoginURL string
	// Started is used to restart the flow before FlowLifetime elapses.
	Started time.Time

	pollToken    string
	pollEndpoint string
}

func (f *Flow) Expired() bool {
	// Restart a little early rather than racing the server's own expiry.
	return time.Since(f.Started) > FlowLifetime-2*time.Minute
}

// Session is the credential a completed flow yields.
type Session struct {
	Server      string `json:"server"`
	LoginName   string `json:"loginName"`
	AppPassword string `json:"appPassword"`
}

// StartFlow begins pairing. It needs no credentials, which is the point: the
// frame proves nothing, the user authenticates in their own browser.
func StartFlow(ctx context.Context, server string) (*Flow, error) {
	server = strings.TrimSuffix(server, "/")

	req, err := http.NewRequestWithContext(ctx, "POST", server+"/index.php/login/v2", nil)
	if err != nil {
		return nil, err
	}
	// Nextcloud names the resulting app password after the User-Agent, so this
	// is what the user sees in their security settings.
	req.Header.Set("User-Agent", "picture-frame")

	var redirectedTo string
	resp, err := newFlowClient(&redirectedTo).Do(req)
	if err != nil {
		return nil, fmt.Errorf("start login flow: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("start login flow: %s%s", resp.Status,
			redirectHint(server, redirectedTo))
	}

	var payload struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("start login flow: %w", err)
	}
	if payload.Login == "" || payload.Poll.Token == "" || payload.Poll.Endpoint == "" {
		return nil, errors.New("start login flow: incomplete response")
	}

	return &Flow{
		LoginURL:     payload.Login,
		Started:      time.Now(),
		pollToken:    payload.Poll.Token,
		pollEndpoint: payload.Poll.Endpoint,
	}, nil
}

// Poll checks whether the user has approved yet.
//
// The server answers 404 until approval and 200 exactly once afterwards, so a
// successful result must be persisted before anything else can go wrong — there
// is no second chance to read the app password.
func (f *Flow) Poll(ctx context.Context) (*Session, error) {
	form := url.Values{"token": {f.pollToken}}

	req, err := http.NewRequestWithContext(ctx, "POST", f.pollEndpoint,
		bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "picture-frame")

	var redirectedTo string
	resp, err := newFlowClient(&redirectedTo).Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll login flow: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var session Session
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&session); err != nil {
			return nil, fmt.Errorf("poll login flow: %w", err)
		}
		if session.AppPassword == "" || session.LoginName == "" {
			return nil, errors.New("poll login flow: incomplete session")
		}
		if session.Server == "" {
			session.Server = strings.TrimSuffix(f.pollEndpoint, "/index.php/login/v2/poll")
		}
		return &session, nil

	case http.StatusNotFound:
		if f.Expired() {
			return nil, ErrFlowExpired
		}
		return nil, ErrFlowPending

	default:
		return nil, fmt.Errorf("poll login flow: %s%s", resp.Status,
			redirectHint(f.pollEndpoint, redirectedTo))
	}
}
