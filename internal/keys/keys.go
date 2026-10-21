// Package keys finds the ssh key pairs in ~/.ssh and what's known about
// each: type, size, fingerprint, whether it has a passphrase and whether
// the agent holds it. Public keys are parsed here; of a private key only
// the header is read, to see whether it's encrypted.
package keys

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Protection is whether a private key needs a passphrase.
type Protection int

const (
	ProtectionUnknown Protection = iota
	Passphrase
	NoPassphrase
)

// Key is a key pair, or half of one.
type Key struct {
	// Name is the private key's file name ("id_ed25519"), or the public
	// key's without .pub when there's no private key.
	Name    string
	Path    string // the private key; "" when only the .pub exists
	PubPath string // "" when there's no .pub
	// Public is the .pub line: type, base64 blob and comment.
	Public      string
	Type        string // as ssh-keygen shows it: ED25519, RSA, ECDSA-SK...
	Bits        int
	Fingerprint string // SHA256:...
	Comment     string
	Protection  Protection
	// Err is why the public key couldn't be read.
	Err error
}

// Blob is the base64 key itself, without the type and comment: what
// identifies the key in authorized_keys.
func (k *Key) Blob() string {
	if f := strings.Fields(k.Public); len(f) >= 2 {
		return f[1]
	}
	return ""
}

// Dir is ~/.ssh.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh")
}

// notKeys are files in ~/.ssh that are never keys.
var notKeys = map[string]bool{
	"config": true, "known_hosts": true, "known_hosts.old": true, "authorized_keys": true,
	"authorized_keys2": true, "environment": true, "rc": true,
}

// maxKeyFile is the largest file looked at; keys are a few KB at most.
const maxKeyFile = 64 << 10

// Scan finds the key pairs in dir, sorted by name. A missing dir has no
// keys.
func Scan(dir string) ([]*Key, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	byName := map[string]*Key{}
	get := func(name string) *Key {
		if k := byName[name]; k != nil {
			return k
		}
		k := &Key{Name: name}
		byName[name] = k
		return k
	}
	for _, e := range entries {
		name := e.Name()
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxKeyFile || notKeys[name] {
			continue
		}
		path := filepath.Join(dir, name)
		if base, ok := strings.CutSuffix(name, ".pub"); ok {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			k := get(base)
			k.PubPath = path
			k.Public = strings.TrimSpace(firstLine(string(data)))
			if k.Type, k.Bits, k.Fingerprint, k.Comment, k.Err = ParsePublic(k.Public); k.Err != nil {
				k.Err = fmt.Errorf("%s: %w", name, k.Err)
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data[:min(len(data), 100)], []byte("PRIVATE KEY-----")) {
			continue
		}
		k := get(name)
		k.Path = path
		k.Protection = protection(data)
	}
	var keys []*Key
	for _, k := range byName {
		// A lone .pub only counts when it really is a public key (not, say,
		// a .pub of something else).
		if k.Path == "" && k.Err != nil {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// ParsePublic reads an authorized_keys-style line ("ssh-ed25519 AAAA...
// comment") and returns what ssh-keygen -l would show.
func ParsePublic(line string) (typ string, bits int, fingerprint, comment string, err error) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return "", 0, "", "", errors.New("not a public key")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return "", 0, "", "", errors.New("not a public key")
	}
	r := wire(blob)
	name, ok := r.next()
	if !ok || string(name) != f[0] {
		return "", 0, "", "", errors.New("not a public key")
	}
	sum := sha256.Sum256(blob)
	fingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	comment = strings.Join(f[2:], " ")

	switch n := string(name); {
	case n == "ssh-rsa":
		r.next() // e
		modulus, _ := r.next()
		typ, bits = "RSA", new(big.Int).SetBytes(modulus).BitLen()
	case n == "ssh-dss":
		p, _ := r.next()
		typ, bits = "DSA", new(big.Int).SetBytes(p).BitLen()
	case strings.HasPrefix(n, "ecdsa-sha2-nistp"):
		typ, bits = "ECDSA", curveBits(strings.TrimPrefix(n, "ecdsa-sha2-"))
	case strings.HasPrefix(n, "sk-ecdsa-sha2-nistp"):
		typ, bits = "ECDSA-SK", curveBits(strings.TrimSuffix(strings.TrimPrefix(n, "sk-ecdsa-sha2-"), "@openssh.com"))
	case n == "ssh-ed25519":
		typ, bits = "ED25519", 256
	case n == "sk-ssh-ed25519@openssh.com":
		typ, bits = "ED25519-SK", 256
	default:
		typ = strings.ToUpper(strings.TrimPrefix(n, "ssh-"))
	}
	return typ, bits, fingerprint, comment, nil
}

func curveBits(curve string) int {
	switch curve {
	case "nistp256":
		return 256
	case "nistp384":
		return 384
	case "nistp521":
		return 521
	}
	return 0
}

// wire reads ssh's length-prefixed strings.
type wire []byte

func (w *wire) next() ([]byte, bool) {
	if len(*w) < 4 {
		return nil, false
	}
	n := binary.BigEndian.Uint32(*w)
	if uint64(len(*w)-4) < uint64(n) {
		return nil, false
	}
	s := (*w)[4 : 4+n]
	*w = (*w)[4+n:]
	return s, true
}

// protection reads a private key's header: OpenSSH keys name their
// cipher ("none" when unencrypted), PEM keys carry Proc-Type or say
// ENCRYPTED.
func protection(data []byte) Protection {
	block, _ := pem.Decode(data)
	if block == nil {
		return ProtectionUnknown
	}
	switch {
	case block.Type == "OPENSSH PRIVATE KEY":
		const magic = "openssh-key-v1\x00"
		if !bytes.HasPrefix(block.Bytes, []byte(magic)) {
			return ProtectionUnknown
		}
		r := wire(block.Bytes[len(magic):])
		cipher, ok := r.next()
		switch {
		case !ok:
			return ProtectionUnknown
		case string(cipher) == "none":
			return NoPassphrase
		}
		return Passphrase
	case block.Type == "ENCRYPTED PRIVATE KEY", strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED"):
		return Passphrase
	case strings.HasSuffix(block.Type, "PRIVATE KEY"):
		return NoPassphrase
	}
	return ProtectionUnknown
}

// UsedBy reports whether identityFiles (as `ssh -G` prints them, with ~)
// include the key.
func (k *Key) UsedBy(identityFiles []string, expand func(string) string) bool {
	path := k.Path
	if path == "" {
		path = strings.TrimSuffix(k.PubPath, ".pub")
	}
	for _, f := range identityFiles {
		if samePath(expand(f), path) || samePath(expand(f), k.PubPath) {
			return true
		}
	}
	return false
}
