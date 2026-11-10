package app

// configSyncState is the in-memory state of configuration sync.
type configSyncState struct {
	// secrets replaces Secrets for this service (tests); nil = Secrets.
	secrets SecretStore
	links   linkSecrets // links this run put into the keychain (configsecrets.go)
}
