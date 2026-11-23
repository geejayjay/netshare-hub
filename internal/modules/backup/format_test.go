package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

const pass = "correct horse battery"

func TestSealOpen(t *testing.T) {
	payload := []byte(`{"format":"netshare-hub-config","users":[]}`)
	file, err := seal(payload, pass)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file, []byte("netshare-hub-config")) {
		t.Fatal("the payload is readable in the file")
	}
	got, err := open(file, pass)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("open: %q %v", got, err)
	}
	// Two backups of the same thing never look alike (fresh salt and nonce).
	again, _ := seal(payload, pass)
	if bytes.Equal(again, file) {
		t.Error("same file twice")
	}
}

func TestOpenRefuses(t *testing.T) {
	file, err := seal([]byte(`{"a":1}`), pass)
	if err != nil {
		t.Fatal(err)
	}
	edit := func(change func(e *envelope)) []byte {
		var e envelope
		if err := json.Unmarshal(file, &e); err != nil {
			t.Fatal(err)
		}
		change(&e)
		out, _ := json.Marshal(e)
		return out
	}
	cases := []struct {
		name string
		file []byte
		pass string
		want error
	}{
		{"wrong passphrase", file, "not the passphrase", errUnreadable},
		{"one byte of data changed", edit(func(e *envelope) { e.Data[len(e.Data)/2] ^= 1 }), pass, errUnreadable},
		{"header changed (key cost lowered)", edit(func(e *envelope) { e.KDF.Time = 2 }), pass, errUnreadable},
		{"newer version", edit(func(e *envelope) { e.Version = 2 }), pass, errTooNew},
		{"huge key cost", edit(func(e *envelope) { e.KDF.MemoryKiB = 4 << 20 }), pass, errNotBackup},
		{"other cipher", edit(func(e *envelope) { e.Cipher = "none" }), pass, errNotBackup},
		{"not a backup", []byte(`{"hello":"world"}`), pass, errNotBackup},
		{"not JSON", []byte("PK\x03\x04 a zip file"), pass, errNotBackup},
		{"too large", make([]byte, MaxFile+1), pass, errTooLarge},
	}
	for _, c := range cases {
		if _, err := open(c.file, c.pass); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}
