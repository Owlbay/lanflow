package main

import (
	"errors"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
)

// The WebDAV password goes to the system's credential store through
// MyGo's Secrets module (the login Keychain on macOS, the Credential
// Manager on Windows). Package app only knows the SecretStore interface,
// so tests run without a keychain; where the system has no store the
// service falls back to an obfuscated 0600 file (app/backup.go).

type mygoSecrets struct{}

func (mygoSecrets) Get(key string) ([]byte, error) {
	v, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, app.ErrSecretNotFound
	}
	return v, err
}

func (mygoSecrets) Set(key string, value []byte) error { return mygo.Secrets.Set(key, value) }
func (mygoSecrets) Delete(key string) error            { return mygo.Secrets.Delete(key) }

func init() { app.Secrets = mygoSecrets{} }
