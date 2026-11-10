package ca

// SystemKeys is the platform's key store for the root: a non-exportable
// CNG key on Windows (keys_windows.go), the secret store elsewhere
// (the login Keychain on macOS).
func SystemKeys(secrets SecretStore) KeyStore { return systemKeys(secrets) }
