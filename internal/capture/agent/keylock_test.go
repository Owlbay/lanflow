package agent

import (
	"crypto"
	"errors"
	"testing"
	"time"

	"lanflow/internal/capture/ca"
)

// lockedKeys refuses to open any key, as the Keychain does for a new
// build the user has not allowed yet.
type lockedKeys struct {
	ca.KeyStore
	err     error
	creates int
}

func (k *lockedKeys) Open(string) (crypto.Signer, error) { return nil, k.err }
func (k *lockedKeys) Create(name string) (crypto.Signer, error) {
	k.creates++
	return k.KeyStore.Create(name)
}

// A key that is there but cannot be read keeps the root (it stays
// trusted) and is reported; a missing or corrupt key is replaced.
func TestLoadRootKeepsALockedKey(t *testing.T) {
	mem := &ca.MemKeys{}
	_, pemb, err := ca.Generate(mem, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st := Start{CAPEM: string(pemb), Host: "test"}

	locked := &lockedKeys{KeyStore: mem, err: errors.New("keychain: user interaction is not allowed (-25308)")}
	if _, _, created, err := loadRoot(locked, st, false); !errors.Is(err, errKeyLocked) || created || locked.creates != 0 {
		t.Fatalf("locked key: err=%v created=%v creates=%d", err, created, locked.creates)
	}

	for name, e := range map[string]error{"missing": ca.ErrNoKey, "corrupt": ca.ErrKeyMismatch} {
		ks := &lockedKeys{KeyStore: mem, err: e}
		_, _, created, err := loadRoot(ks, st, false)
		if err != nil || !created || ks.creates != 1 {
			t.Fatalf("%s key: err=%v created=%v creates=%d", name, err, created, ks.creates)
		}
	}
}
