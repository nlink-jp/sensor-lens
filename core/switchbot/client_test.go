package switchbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testClient returns a Client pinned to a fixed clock and nonce so the
// signature it sends is predictable.
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return New("TESTTOKEN", "TESTSECRET",
		WithBaseURL(srv.URL),
		WithClock(func() time.Time { return time.UnixMilli(1700000000000) }),
		WithNonce(func() (string, error) { return "3f2504e0-4f89-41d3-9a0c-0305e82c3301", nil }),
	)
}

func TestDevicesSendsSignedHeaders(t *testing.T) {
	var got http.Header
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"statusCode":100,"message":"success","body":{"deviceList":[],"infraredRemoteList":[]}}`))
	})

	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatalf("Devices() error = %v", err)
	}

	want := map[string]string{
		"Authorization": "TESTTOKEN",
		"Sign":          "XJ56R0XVD+WJKKS/8TWKUTHBBO3KKUJGPUUYIIB9M34=",
		"Nonce":         "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"T":             "1700000000000",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestDevicesDropsInfraredRemotes(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"statusCode":100,"message":"success","body":{
			"deviceList":[
				{"deviceId":"AAA","deviceName":"Living Room","deviceType":"MeterPro(CO2)","enableCloudService":true,"hubDeviceId":"HUB1"}
			],
			"infraredRemoteList":[
				{"deviceId":"02-202008110034-13","deviceName":"TV","remoteType":"TV","hubDeviceId":"HUB1"}
			]}}`))
	})

	devices, err := c.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices() error = %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("Devices() returned %d devices, want 1 (infrared remotes must be dropped)", len(devices))
	}
	want := Device{DeviceID: "AAA", DeviceName: "Living Room", DeviceType: "MeterPro(CO2)", EnableCloudService: true, HubDeviceID: "HUB1"}
	if devices[0] != want {
		t.Errorf("Devices()[0] = %+v, want %+v", devices[0], want)
	}
}

func TestDeviceStatusReturnsRawBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.1/devices/AAA/status" {
			t.Errorf("path = %q, want /v1.1/devices/AAA/status", r.URL.Path)
		}
		w.Write([]byte(`{"statusCode":100,"message":"success","body":{
			"deviceId":"AAA","deviceType":"MeterPro(CO2)","hubDeviceId":"HUB1",
			"temperature":22.5,"humidity":31,"CO2":1203,"battery":100,"version":"V4.2"}}`))
	})

	body, err := c.DeviceStatus(context.Background(), "AAA")
	if err != nil {
		t.Fatalf("DeviceStatus() error = %v", err)
	}
	if body["CO2"] != float64(1203) {
		t.Errorf("body[CO2] = %v, want 1203", body["CO2"])
	}
	// Unknown-to-us fields must survive: core/metrics decides what to keep.
	if body["version"] != "V4.2" {
		t.Errorf("body[version] = %v, want V4.2", body["version"])
	}
}

func TestDeviceStatusRejectsEmptyID(t *testing.T) {
	c := New("t", "s")
	if _, err := c.DeviceStatus(context.Background(), ""); err == nil {
		t.Fatal("DeviceStatus(\"\") succeeded, want error")
	}
}

func TestMissingCredentials(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request issued despite missing credentials")
	})
	c.token = ""

	if _, err := c.Devices(context.Background()); err == nil {
		t.Fatal("Devices() succeeded without a token, want error")
	}
}

func TestEnvelopeErrors(t *testing.T) {
	tests := []struct {
		name          string
		httpStatus    int
		body          string
		wantStatus    int
		wantTransient bool
		wantLimited   bool
	}{
		{"device type", 200, `{"statusCode":151,"message":"device type error","body":{}}`, 151, false, false},
		{"not found", 200, `{"statusCode":152,"message":"device not found","body":{}}`, 152, false, false},
		{"device offline", 200, `{"statusCode":161,"message":"device offline","body":{}}`, 161, true, false},
		{"hub offline", 200, `{"statusCode":171,"message":"hub device offline","body":{}}`, 171, true, false},
		{"system error", 200, `{"statusCode":190,"message":"system error","body":{}}`, 190, true, false},
		// 401 is ambiguous by design: bad token *or* the daily quota spent.
		{"unauthorized", 401, `Unauthorized`, 0, false, true},
		{"too many requests", 429, `Too Many Requests`, 0, true, true},
		{"server error", 503, `Service Unavailable`, 0, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.httpStatus)
				w.Write([]byte(tc.body))
			})

			_, err := c.DeviceStatus(context.Background(), "AAA")
			apiErr, ok := AsAPIError(err)
			if !ok {
				t.Fatalf("DeviceStatus() error = %v, want *APIError", err)
			}
			if apiErr.StatusCode != tc.wantStatus {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.wantStatus)
			}
			if apiErr.Transient() != tc.wantTransient {
				t.Errorf("Transient() = %v, want %v", apiErr.Transient(), tc.wantTransient)
			}
			if apiErr.RateLimited() != tc.wantLimited {
				t.Errorf("RateLimited() = %v, want %v", apiErr.RateLimited(), tc.wantLimited)
			}
			if apiErr.Error() == "" {
				t.Error("Error() is empty")
			}
		})
	}
}

func TestCallsCounted(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"statusCode":100,"message":"success","body":{"deviceList":[]}}`))
	})

	for range 3 {
		if _, err := c.Devices(context.Background()); err != nil {
			t.Fatalf("Devices() error = %v", err)
		}
	}
	// The daily quota counts requests, not successes: a 401 still spends one.
	if c.Calls() != 3 {
		t.Errorf("Calls() = %d, want 3", c.Calls())
	}
}
