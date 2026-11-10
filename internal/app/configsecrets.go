package app

import (
	"errors"
	"sync"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Secrets of configuration sync (docs/config-sync.md 密钥). lanflow.yaml
// never holds a subscription link or the WebDAV password: links go into
// the system keychain under sub-<id> (Secrets: the login Keychain on
// macOS, the Credential Manager on Windows) and the file says
// keychain:sub-<id>; the WebDAV password already lives there
// (backup.go). profiles.json keeps its own copy of the links, as before.
//
// Another machine that reads a reference its keychain does not have
// gets the subscription without a link (Subscription.NeedsURL) and the
// user enters it again; nothing fails. The items are not marked
// kSecAttrSynchronizable: iCloud Keychain only syncs items of the data
// protection keychain, which needs an app signed with a team's
// keychain-access-groups entitlement (this app is not), and MyGo's
// Secrets module has no way to set the attribute.

// errNeedsURL is what updating a subscription without its link says.
var errNeedsURL = i18n.Error("这个订阅来自配置同步，本机钥匙串里没有它的链接。请在「编辑」中重新输入订阅链接。")

// linkSecrets remembers what this run put into the keychain, so the
// file can be rewritten often without writing the keychain each time.
type linkSecrets struct {
	mu     sync.Mutex
	stored map[string]string // key → value written
}

// storeLinkSecrets puts the link of every URL subscription in subs into
// the keychain. It returns the first error (no keychain, or the user
// refused access): the file is still written with references, and this
// machine keeps working from profiles.json.
func (s *Service) storeLinkSecrets(subs []model.Subscription) error {
	ks := s.secretStore()
	ls := &s.cs.links
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.stored == nil {
		ls.stored = map[string]string{}
	}
	var first error
	for _, sub := range subs {
		if sub.Source != urlSubscriptionK || sub.URL == "" {
			continue
		}
		key := syncfile.SubscriptionKey(sub.ID)
		if ls.stored[key] == sub.URL {
			continue
		}
		if ks == nil {
			if first == nil {
				first = errors.ErrUnsupported
			}
			continue
		}
		if err := ks.Set(key, []byte(sub.URL)); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		ls.stored[key] = sub.URL
	}
	return first
}

// forgetLinkSecret removes a deleted subscription's link from the
// keychain (best effort; only for links this machine stored or synced).
func (s *Service) forgetLinkSecret(id string) {
	ks := s.secretStore()
	if ks == nil {
		return
	}
	key := syncfile.SubscriptionKey(id)
	ls := &s.cs.links
	ls.mu.Lock()
	delete(ls.stored, key)
	ls.mu.Unlock()
	if err := ks.Delete(key); err != nil {
		s.alog.Warnf("config sync: keychain delete %s: %v", key, err)
	}
}

// resolveLink returns the link a document's subscription entry stands
// for: the link itself, the keychain's value for a reference, or — when
// this machine's keychain lacks it — the link this machine already has
// for the same subscription (local). "" means the user must enter it.
func (s *Service) resolveLink(ref, local string) string {
	if ref == "" {
		return local
	}
	if _, ok := syncfile.KeychainKey(ref); !ok {
		return ref // a plain link (written by a person or an agent)
	}
	if v := s.resolveRef(ref); v != "" {
		// remember it: the keychain has it, no need to write it back
		ls := &s.cs.links
		ls.mu.Lock()
		if ls.stored == nil {
			ls.stored = map[string]string{}
		}
		key, _ := syncfile.KeychainKey(ref)
		ls.stored[key] = v
		ls.mu.Unlock()
		return v
	}
	return local
}
