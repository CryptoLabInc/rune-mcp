package seal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestSealV2FixedConsoleFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/metadata_v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		BaseKey   string `json:"base_key_hex"`
		Nonce     string `json:"nonce_hex"`
		Plaintext string `json:"plaintext"`
		Agent     string `json:"agent_id"`
		Record    string `json:"record_id"`
		Envelope  string `json:"envelope_json"`
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(f.BaseKey)
	nonce, _ := hex.DecodeString(f.Nonce)
	// A fixed test-only entropy source proves the writer matches the independently
	// generated Console fixture, including HKDF, AAD and ciphertext/tag bytes.
	previous := rand.Reader
	rand.Reader = bytes.NewReader(nonce)
	defer func() { rand.Reader = previous }()
	got, err := SealV2(key, f.Agent, f.Record, []byte(f.Plaintext))
	if err != nil {
		t.Fatal(err)
	}
	if got != f.Envelope {
		t.Fatalf("fixture disagreement:\n got %s\nwant %s", got, f.Envelope)
	}
}

func TestSealV2FreshNonceAndEnvelope(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	first, err := SealV2(key, "agent", "record", []byte("private"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := SealV2(key, "agent", "record", []byte("private"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("reused nonce")
	}
	var env struct {
		V int    `json:"v"`
		A string `json:"a"`
		R string `json:"r"`
		C string `json:"c"`
	}
	if err = json.Unmarshal([]byte(first), &env); err != nil {
		t.Fatal(err)
	}
	if env.V != 2 || env.A != "agent" || env.R != "record" {
		t.Fatalf("invalid envelope: %+v", env)
	}
	payload, err := base64.StdEncoding.DecodeString(env.C)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 12+len("private")+16 {
		t.Fatalf("payload length=%d", len(payload))
	}
	// Raw base DEK must not decrypt: v2 separates its encryption key with HKDF.
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if _, err = gcm.Open(nil, payload[:12], payload[12:], []byte(`["rune-metadata","2","agent","record"]`)); err == nil {
		t.Fatal("used base DEK directly")
	}
}

func TestSealV2RejectInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		key           []byte
		agent, record string
	}{
		{"nil key", nil, "a", "r"}, {"short key", make([]byte, 31), "a", "r"}, {"long key", make([]byte, 33), "a", "r"}, {"empty agent", make([]byte, 32), "", "r"}, {"empty record", make([]byte, 32), "a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SealV2(tc.key, tc.agent, tc.record, nil)
			if err == nil || got != "" {
				t.Fatalf("accepted invalid input: %q %v", got, err)
			}
		})
	}
}
