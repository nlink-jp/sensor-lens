package switchbot

import (
	"regexp"
	"testing"
)

func TestSign(t *testing.T) {
	// Expected value computed independently of this package:
	//   printf '%s' 'TESTTOKEN17000000000003f2504e0-4f89-41d3-9a0c-0305e82c3301' \
	//     | openssl dgst -sha256 -hmac 'TESTSECRET' -binary | base64 \
	//     | tr '[:lower:]' '[:upper:]'
	const (
		token  = "TESTTOKEN"
		secret = "TESTSECRET"
		ts     = int64(1700000000000)
		nonce  = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
		want   = "XJ56R0XVD+WJKKS/8TWKUTHBBO3KKUJGPUUYIIB9M34="
	)

	if got := Sign(token, secret, ts, nonce); got != want {
		t.Errorf("Sign() = %q, want %q", got, want)
	}
}

func TestSignVariesWithEveryInput(t *testing.T) {
	base := Sign("token", "secret", 1700000000000, "nonce")

	cases := map[string]string{
		"token":     Sign("token2", "secret", 1700000000000, "nonce"),
		"secret":    Sign("token", "secret2", 1700000000000, "nonce"),
		"timestamp": Sign("token", "secret", 1700000000001, "nonce"),
		"nonce":     Sign("token", "secret", 1700000000000, "nonce2"),
	}
	for changed, got := range cases {
		if got == base {
			t.Errorf("changing %s did not change the signature", changed)
		}
	}
}

var uuidV4RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewNonce(t *testing.T) {
	seen := make(map[string]bool, 100)
	for range 100 {
		got, err := newNonce()
		if err != nil {
			t.Fatalf("newNonce() error = %v", err)
		}
		if !uuidV4RE.MatchString(got) {
			t.Fatalf("newNonce() = %q, not a v4 UUID", got)
		}
		if seen[got] {
			t.Fatalf("newNonce() repeated %q", got)
		}
		seen[got] = true
	}
}
