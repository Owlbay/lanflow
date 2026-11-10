//go:build !windows

package ca

func systemKeys(secrets SecretStore) KeyStore { return SecretKeys{S: secrets} }
