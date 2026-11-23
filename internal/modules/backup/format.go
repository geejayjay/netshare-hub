package backup

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// The file format. A backup holds every password hash (and, when chosen,
// every Samba password hash, which is password-equivalent for SMB), so it
// is always encrypted with a passphrase the admin chooses:
//
//	{"format":"netshare-hub-backup","version":1,
//	 "kdf":{"name":"argon2id","time":3,"memoryKiB":65536,"threads":4,"salt":"…"},
//	 "cipher":"xchacha20-poly1305","nonce":"…","data":"…"}
//
// data is the gzip-compressed payload (payload.go), encrypted and
// authenticated; the header is authenticated too. A wrong passphrase and a
// file changed by even one byte look the same: refused, nothing read.

const (
	fileFormat    = "netshare-hub-backup"
	formatVersion = 1
	cipherName    = "xchacha20-poly1305"
	kdfName       = "argon2id"

	// MaxFile is the largest backup file accepted, and maxPayload the most
	// it may unpack to (so a crafted file cannot fill the memory).
	MaxFile    = 16 << 20
	maxPayload = 64 << 20

	// MinPassphrase is the shortest passphrase. The file may end up
	// anywhere, so it must stand up to guessing offline.
	MinPassphrase = 12
	MaxPassphrase = 1024
)

// KDF parameters for new backups: slow on purpose (about a quarter of a
// second), to make guessing the passphrase expensive.
var newKDF = kdf{Name: kdfName, Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

type kdf struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memoryKiB"`
	Threads   uint8  `json:"threads"`
	Salt      []byte `json:"salt"`
}

type envelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	KDF     kdf    `json:"kdf"`
	Cipher  string `json:"cipher"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

var (
	errNotBackup  = errors.New("not a NetShare Hub backup")
	errTooNew     = errors.New("backup made by a newer version")
	errUnreadable = errors.New("wrong passphrase, or the file is damaged")
	errTooLarge   = errors.New("backup too large")
)

// header is what the encryption authenticates besides the data, so the
// format, version and key parameters cannot be changed either.
func (e envelope) header() []byte {
	return fmt.Appendf(nil, "%s|%d|%s|%d|%d|%d|%x|%s", e.Format, e.Version, e.KDF.Name, e.KDF.Time, e.KDF.MemoryKiB,
		e.KDF.Threads, e.KDF.Salt, e.Cipher)
}

func (k kdf) key(passphrase string) []byte {
	return argon2.IDKey([]byte(passphrase), k.Salt, k.Time, k.MemoryKiB, k.Threads, chacha20poly1305.KeySize)
}

// seal compresses and encrypts a payload.
func seal(payload []byte, passphrase string) ([]byte, error) {
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	e := envelope{Format: fileFormat, Version: formatVersion, KDF: newKDF, Cipher: cipherName}
	e.KDF.Salt = make([]byte, 16)
	e.Nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(e.KDF.Salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(e.Nonce); err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(e.KDF.key(passphrase))
	if err != nil {
		return nil, err
	}
	e.Data = aead.Seal(nil, e.Nonce, packed.Bytes(), e.header())
	return json.MarshalIndent(e, "", "  ")
}

// open checks, decrypts and unpacks a backup file.
func open(file []byte, passphrase string) ([]byte, error) {
	if len(file) > MaxFile {
		return nil, errTooLarge
	}
	var e envelope
	dec := json.NewDecoder(bytes.NewReader(file))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil || e.Format != fileFormat {
		return nil, errNotBackup
	}
	switch {
	case e.Version > formatVersion:
		return nil, errTooNew
	case e.Version < 1,
		e.Cipher != cipherName,
		len(e.Nonce) != chacha20poly1305.NonceSizeX,
		// Bounds on the key parameters, so a crafted file cannot make the
		// server spend minutes or gigabytes deriving a key.
		e.KDF.Name != kdfName,
		e.KDF.Time < 1 || e.KDF.Time > 10,
		e.KDF.MemoryKiB < 8*1024 || e.KDF.MemoryKiB > 512*1024,
		e.KDF.Threads < 1 || e.KDF.Threads > 16,
		len(e.KDF.Salt) < 16 || len(e.KDF.Salt) > 64:
		return nil, errNotBackup
	}
	aead, err := chacha20poly1305.NewX(e.KDF.key(passphrase))
	if err != nil {
		return nil, err
	}
	packed, err := aead.Open(nil, e.Nonce, e.Data, e.header())
	if err != nil {
		return nil, errUnreadable
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, errUnreadable
	}
	payload, err := io.ReadAll(io.LimitReader(zr, maxPayload+1))
	if err != nil {
		return nil, errUnreadable
	}
	if len(payload) > maxPayload {
		return nil, errTooLarge
	}
	return payload, nil
}
