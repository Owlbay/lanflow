package main

import (
	"errors"
	"os"

	"github.com/egoist/mygo"

	"lanflow/internal/capture/agent"
	"lanflow/internal/capture/ca"
)

// `lanflow capture-agent`: the HTTPS decryption process the window starts
// (docs/capture.md §5.1). The root's private key exists only here: in the
// login Keychain (MyGo Secrets) on macOS, as a non-exportable CNG key on
// Windows (internal/capture/ca).

// captureSecretService keeps the capture root's key apart from the app's
// other secrets and independent of how the binary was started.
const captureSecretService = "Lanflow Capture CA"

type captureSecrets struct{}

func (captureSecrets) Get(key string) ([]byte, error) {
	v, err := mygo.Secrets.Service(captureSecretService).Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, ca.ErrNoKey
	}
	return v, err
}

func (captureSecrets) Set(key string, value []byte) error {
	return mygo.Secrets.Service(captureSecretService).Set(key, value)
}

func (captureSecrets) Delete(key string) error {
	err := mygo.Secrets.Service(captureSecretService).Delete(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil
	}
	return err
}

func captureAgentMain() int {
	return agent.Main(os.Stdin, os.Stdout, ca.SystemKeys(captureSecrets{}))
}
