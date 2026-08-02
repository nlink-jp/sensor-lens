package switchbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the SwitchBot Open API v1.1 host.
const DefaultBaseURL = "https://api.switch-bot.com"

// Doer is the subset of *http.Client the client needs, so tests can inject a
// transport without a live network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Device is one physical device from GET /v1.1/devices. Infrared remotes are
// not represented: they have no sensor readings.
type Device struct {
	DeviceID           string `json:"deviceId"`
	DeviceName         string `json:"deviceName"`
	DeviceType         string `json:"deviceType"`
	EnableCloudService bool   `json:"enableCloudService"`
	HubDeviceID        string `json:"hubDeviceId"`
}

// Client talks to the SwitchBot Open API v1.1.
type Client struct {
	token   string
	secret  string
	baseURL string
	http    Doer
	now     func() time.Time
	nonce   func() (string, error)

	// calls counts the HTTP requests actually issued, so callers can hold the
	// daily quota without re-deriving it from the schedule.
	calls int
}

// Option customizes a Client. The defaults are the live API over
// http.DefaultClient with a 20 s timeout.
type Option func(*Client)

// WithBaseURL points the client at another host (an httptest server in tests).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithHTTPClient injects the transport.
func WithHTTPClient(d Doer) Option {
	return func(c *Client) { c.http = d }
}

// WithClock injects the time source used for the signature timestamp.
func WithClock(now func() time.Time) Option {
	return func(c *Client) { c.now = now }
}

// WithNonce injects the nonce generator, making requests reproducible in tests.
func WithNonce(fn func() (string, error)) Option {
	return func(c *Client) { c.nonce = fn }
}

// New builds a client for the given credentials.
func New(token, secret string, opts ...Option) *Client {
	c := &Client{
		token:   token,
		secret:  secret,
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: 20 * time.Second},
		now:     time.Now,
		nonce:   newNonce,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Calls returns how many HTTP requests this client has issued.
func (c *Client) Calls() int { return c.calls }

// Devices lists the physical devices on the account. Infrared remotes are
// dropped.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var body struct {
		DeviceList []Device `json:"deviceList"`
	}
	if err := c.get(ctx, "/v1.1/devices", &body); err != nil {
		return nil, err
	}
	return body.DeviceList, nil
}

// DeviceStatus returns the raw status body of one device.
//
// The body is deliberately left as a map: every device type carries a
// different set of fields, and new firmware adds more. Interpreting it is
// core/metrics' job, which keeps this package free of per-model knowledge.
func (c *Client) DeviceStatus(ctx context.Context, deviceID string) (map[string]any, error) {
	if deviceID == "" {
		return nil, fmt.Errorf("switchbot: empty device id")
	}
	var body map[string]any
	if err := c.get(ctx, "/v1.1/devices/"+deviceID+"/status", &body); err != nil {
		return nil, err
	}
	return body, nil
}

// get issues a signed GET and unwraps the {statusCode, message, body} envelope
// into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("switchbot: build request: %w", err)
	}
	if err := c.authorize(req); err != nil {
		return err
	}

	c.calls++
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("switchbot: %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("switchbot: %s: read body: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		return &APIError{HTTPStatus: resp.StatusCode, Message: firstLine(raw)}
	}

	var env struct {
		StatusCode int             `json:"statusCode"`
		Message    string          `json:"message"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("switchbot: %s: decode envelope: %w", path, err)
	}
	if env.StatusCode != StatusSuccess {
		return &APIError{StatusCode: env.StatusCode, HTTPStatus: resp.StatusCode, Message: env.Message}
	}
	if len(env.Body) == 0 {
		return &APIError{StatusCode: env.StatusCode, HTTPStatus: resp.StatusCode, Message: "empty body"}
	}
	if err := json.Unmarshal(env.Body, out); err != nil {
		return fmt.Errorf("switchbot: %s: decode body: %w", path, err)
	}
	return nil
}

// authorize sets the four headers the API authenticates on.
func (c *Client) authorize(req *http.Request) error {
	if c.token == "" || c.secret == "" {
		return fmt.Errorf("switchbot: missing token or secret (see `sensor-lens doctor`)")
	}
	nonce, err := c.nonce()
	if err != nil {
		return err
	}
	ts := c.now().UnixMilli()

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)
	req.Header.Set("sign", Sign(c.token, c.secret, ts, nonce))
	req.Header.Set("nonce", nonce)
	req.Header.Set("t", strconv.FormatInt(ts, 10))
	return nil
}

// firstLine trims a non-JSON error body down to something loggable. The API
// answers plain "Unauthorized" for both a bad token and an exhausted daily
// quota, so the text is worth keeping.
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
