package i18n

// English of agent access: the consent dialog (internal/view/agents.go)
// and 设置 › 高级 › Agent 接入 (internal/view/settings/agents.go).
func init() {
	for k, v := range map[string]string{
		// internal/view/agents.go
		"有 agent 请求授权":    "An agent asks for permission",
		"允许「%s」读取敏感内容吗？":  "Allow \"%s\" to read sensitive data?",
		"允许「%s」执行这项操作吗？":  "Allow \"%s\" to perform this action?",
		"允许「%s」修改澜流的配置吗？": "Allow \"%s\" to change Lanflow's configuration?",
		"它请求读取连接明细、日志原文或订阅内容等敏感信息。允许只对这次连接有效。": "It asks to read sensitive data such as connection details, raw logs or subscription contents. Allowing applies to this connection only.",
		"这是一项影响较大的操作，只对这一次请求有效。":               "This action has wide effects; allowing applies to this request only.",
		"它想通过本机接口修改设置、规则、节点或订阅。每次修改都会记入审计日志，可以用 lanflow ctl undo 撤销。选择「始终允许」后，可在 设置 › 高级 › Agent 接入 撤销。": "It wants to change settings, rules, nodes or subscriptions through the local interface. Every change goes to the audit log and can be reverted with lanflow ctl undo. \"Always allow\" can be revoked in Settings › Advanced › Agent access.",
		"仅本次":  "Just this time",
		"始终允许": "Always allow",
		"允许":   "Allow",

		// internal/view/settings/agents.go
		"Agent 接入": "Agent access",
		"Claude 等 agent 可以通过 lanflow mcp 或 lanflow ctl 查看和管理澜流；修改配置前会先请你授权，并记入审计日志": "Agents such as Claude can view and manage Lanflow through lanflow mcp or lanflow ctl; they ask for your permission before changing the configuration, and every change is audited",
		"已授权的 agent":        "Authorized agents",
		"还没有 agent 请求过修改配置": "No agent has asked to change the configuration yet",
		"始终允许修改配置 · %s":     "Always allowed to change the configuration · %s",
		"已拒绝修改配置 · %s":      "Refused changing the configuration · %s",
		"撤销":                "Revoke",
		"已撤销「%s」的授权":        "Revoked the permission of \"%s\"",
		"MCP 配置":            "MCP configuration",
		"复制后粘贴到 Claude Desktop 的 claude_desktop_config.json 或项目的 .mcp.json；也可以复制 claude mcp add 命令在终端运行": "Paste it into Claude Desktop's claude_desktop_config.json or a project's .mcp.json, or copy the claude mcp add command to run in a terminal",
		"复制命令":      "Copy command",
		"复制 MCP 配置": "Copy MCP config",
	} {
		en[k] = v
	}
}
