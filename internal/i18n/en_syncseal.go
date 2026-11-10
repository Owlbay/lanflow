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
	} {
		en[k] = v
	}
}
