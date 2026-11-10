package i18n

// English of the sync passphrase: secrets sealed into lanflow.yaml
// (internal/syncfile/seal.go, internal/app/configseal.go) and its part of
// 设置 › 常规 › 配置同步.
func init() {
	for k, v := range map[string]string{
		// internal/syncfile
		"salt 应为至少 16 字节的 base64":                         "salt should be base64 of at least 16 bytes",
		"webdavPassword 应为 keychain:<id> 引用或 enc:v1: 加密值": "webdavPassword should be a keychain:<id> reference or an enc:v1: sealed value",
		"不支持的密钥派生方式「%s」":                                  "unsupported key derivation \"%s\"",
		"加密的值已损坏，或不属于这个字段。":                               "the encrypted value is damaged, or belongs to another field.",
		"加密的值应为 enc:v1:<base64>":                          "an encrypted value should be enc:v1:<base64>",
		"同步口令不正确。":                                        "The sync passphrase is not correct.",
		"同步口令至少需要 8 个字符。":                                 "The sync passphrase needs at least 8 characters.",
		"密钥派生参数超出允许范围":                                    "the key derivation parameters are out of range",
		"有加密的值，但文件缺少 encryption（口令参数）":                    "there are encrypted values, but the file has no encryption block (passphrase parameters)",
		"订阅链接应为 keychain:<id> 引用、enc:v1: 加密值或 http(s) 链接": "the subscription link should be a keychain:<id> reference, an enc:v1: encrypted value or an http(s) link",
		// internal/app/configseal.go
		"同步文件中没有用口令加密的链接。":          "The sync file has no links encrypted with a passphrase.",
		"同步文件无效：%v":                 "The sync file is invalid: %v",
		"本机没有可用的钥匙串，无法保存同步口令派生的密钥。": "This machine has no keychain to keep the key derived from the sync passphrase.",
		"请先输入当前的同步口令解锁，再修改口令。":      "Enter the current sync passphrase to unlock first, then change it.",
		// internal/view/settings/syncpass.go, configsync.go; overview
		"用口令加密链接": "Encrypt links with a passphrase",
		"把订阅链接和 WebDAV 密码用口令加密后写进同步文件，其他设备（包括 Windows）输入同一口令即可取回。口令本身不保存；丢失口令后，文件中的链接无法恢复，只能在各设备上重新输入。": "Subscription links and the WebDAV password are encrypted with a passphrase and written into the sync file; other devices (Windows too) get them back by entering the same passphrase. The passphrase itself is not stored: if it is lost, the links in the file cannot be recovered and have to be entered again on each device.",
		"关闭时同步文件只写钥匙串引用，另一台设备需要重新输入订阅链接。开启后，链接和 WebDAV 密码用口令加密写进文件；口令丢失后无法恢复这些链接。":                      "When off, the sync file holds keychain references only and another device needs the subscription links entered again. When on, links and the WebDAV password are written into the file encrypted with a passphrase; if the passphrase is lost, they cannot be recovered.",
		"关闭口令加密？": "Turn off passphrase encryption?",
		"同步文件改回只写钥匙串引用，加密的链接和 WebDAV 密码从文件中移除，本机钥匙串里的派生密钥也会删除。其他设备之后需要自己的钥匙串里有链接，否则要重新输入。": "The sync file goes back to keychain references: the encrypted links and WebDAV password are removed from it, and the derived key is deleted from this machine's keychain. Other devices then need the links in their own keychain, or have to enter them again.",
		"关闭加密": "Turn Off",
		"已关闭口令加密；同步文件改回钥匙串引用": "Passphrase encryption is off; the sync file uses keychain references again",
		"同步口令": "Sync passphrase",
		"已在本机保存由口令派生的密钥，启动时无需再次输入。修改口令后，其他设备需要输入新口令。": "The key derived from the passphrase is kept on this machine, so it is not asked again at start. After a change, other devices need the new passphrase.",
		"修改口令…":      "Change Passphrase…",
		"在本机忘记口令":    "Forget on This Device",
		"在本机忘记同步口令？": "Forget the sync passphrase on this machine?",
		"本机钥匙串中由口令派生的密钥会被删除。本机已有的订阅链接保留；之后同步文件中新增或修改的链接，需要重新输入口令才能取回。": "The key derived from the passphrase is deleted from this machine's keychain. The subscription links already here stay; links added or changed in the sync file later need the passphrase again.",
		"忘记口令":                "Forget",
		"已在本机忘记同步口令":          "The sync passphrase is forgotten on this machine",
		"请输入同步口令。":            "Enter the sync passphrase.",
		"两次输入的口令不一致。":         "The two passphrases do not match.",
		"同步口令正确，已解锁订阅链接":      "Passphrase accepted; subscription links unlocked",
		"已用口令加密同步文件中的链接":      "Links in the sync file are now encrypted with the passphrase",
		"同步口令已修改；其他设备需要输入新口令": "Sync passphrase changed; other devices need the new one",
		"同步口令不正确，请重新输入。":      "The sync passphrase is not correct. Try again.",
		"设置同步口令":              "Set Sync Passphrase",
		"订阅链接和 WebDAV 密码会用这个口令加密后写进同步文件。其他设备开启同步后输入同一口令即可取回。": "Subscription links and the WebDAV password are encrypted with this passphrase and written into the sync file. Other devices get them back by entering the same passphrase after turning on sync.",
		"加密":     "Encrypt",
		"修改同步口令": "Change Sync Passphrase",
		"所有链接会用新口令重新加密并写回同步文件；其他设备读到新文件后，需要输入新口令。": "Every link is encrypted again with the new passphrase and written back to the sync file; other devices will ask for the new passphrase when they read it.",
		"修改口令":   "Change",
		"输入同步口令": "Enter Sync Passphrase",
		"输入在其他设备上设置的同步口令，以解锁同步文件中的订阅链接。口令正确后，链接存入本机钥匙串，之后启动无需再次输入。":                                     "Enter the sync passphrase set on another device to unlock the subscription links in the sync file. Once it is right, the links go into this machine's keychain and later starts do not ask again.",
		"口令不会保存在任何地方，澜流也无法找回。口令丢失后，同步文件中的链接无法恢复，只能在各设备上重新输入。同步文件放在第三方云盘上，请使用足够长、不与其他服务共用的口令（至少 8 个字符）。": "The passphrase is not stored anywhere and Lanflow cannot recover it. If it is lost, the links in the sync file cannot be recovered and have to be entered again on each device. The sync file sits on a third-party cloud drive: use a long passphrase you do not use anywhere else (at least 8 characters).",
		"再次输入口令": "Repeat passphrase",
		"输入口令…":  "Enter Passphrase…",
		"同步口令已在另一台设备上修改。输入新口令以解锁订阅链接；在此之前，本机已有的链接照常使用。": "The sync passphrase was changed on another device. Enter the new one to unlock the subscription links; until then the links already on this machine keep working.",
		"输入同步口令以解锁订阅链接（同步文件中有 %d 项加密的链接或密码，本机还不能读取）。":   "Enter the sync passphrase to unlock the subscription links (the sync file has %d encrypted links or passwords this machine cannot read yet).",
		"输入同步口令以解锁订阅链接。": "Enter the sync passphrase to unlock the subscription links.",
	} {
		en[k] = v
	}
}
