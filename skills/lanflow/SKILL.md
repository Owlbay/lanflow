---
name: lanflow
description: Manage the Lanflow (澜流) proxy client running on this computer through its MCP tools or the `lanflow ctl` command — check status, switch nodes, route a domain through a node or group, update subscriptions, change settings, diagnose sites that do not load and fix DNS leaks. Use when the user asks about their proxy, VPN-like connectivity, 澜流/Lanflow, Mihomo/Clash rules, nodes or subscriptions.
---

# Lanflow (澜流)

Lanflow is a desktop proxy client built on Mihomo (Clash Meta). It runs on the user's Mac or PC; you reach it through the `lanflow` MCP server (preferred) or the `lanflow ctl` command line. Both talk to the running app over a private local socket. If a call says Lanflow is not running, ask the user to open it — do not try to start it yourself.

## Tools

Names are the MCP tool names; `lanflow ctl` equivalents in brackets.

**Look (read-only)**
- `status` [`ctl status`] — core state, system proxy, TUN, mode, port, current subscription.
- `diagnose` [`ctl diag`, `ctl diag --run`] — overall snapshot with findings; `run: true` re-tests sites and leaks first.
- `proxies` [`ctl proxies list [group]`] — groups, their selection, every node's last delay (ms; 0 = untested, -1 = failed).
- `subscriptions` [`ctl sub list`] — subscriptions without their links.
- `rules`, `custom_rules`, `rule_providers`, `test_rule` [`ctl rule test <host>`] — which rule and proxy chain a host takes.
- `settings` [`ctl config get [Path]`] — settings, redacted. `effective_dns`, `net_info`, `sites`, `leak_check`, `exit_routes`, `you`, `traffic_stats`, `app_logs`.
- Measurements that change nothing: `test_proxy_delay`, `test_group_delay`, `test_all_delays`, `test_sites`, `run_leak_check`, `dns_query`, `probe_ip`, `probe_unlock`, `refresh_network`.

**Change (asks the user the first time, audited, undoable)**
- `select_proxy` [`ctl proxies select <group> <node>`], `set_mode` [`ctl mode set rule|global|direct`], `set_main_group`.
- `save_custom_rule`, `delete_custom_rule`, `move_custom_rule` [`ctl rule add <TYPE> <payload> <target> [--dry-run]`].
- `patch_settings` [`ctl config set <Path> <value> [--dry-run]`, `ctl config apply <file> [--dry-run]`] — change settings field by field, e.g. `{"Mode":"rule","DNS":{"TakeOver":true}}`. `dryRun: true` returns the diff only.
- Subscriptions: `import_subscription_url` [`ctl sub add <url> [--name] [--use]`], `use_subscription` [`ctl sub use`], `update_subscription` / `update_all_subscriptions` [`ctl sub update [--all]`].
- Core and switches: `start_core`, `stop_core`, `restart_core`, `set_system_proxy`, `set_tun`.
- `undo` [`ctl undo`] — reverts the latest write (settings, custom rules, overrides, node selection, current subscription). `audit_log` [`ctl audit`] lists recent writes.

**Composite (prefer these)**
- `route_domain` — make a domain go through a node, group, DIRECT or REJECT: rule test → rule → diff → apply → verify. Supports `dry_run`.
- `diagnose_site` — why one site does not load: rule, chain, DNS answer, exit node delay, findings.
- `switch_node` — select a node in a group (or the fastest after testing) and measure it.

Sensitive reads (`connections`, `logs`, `subscription_raw`, `runtime_config`) need the user's approval in Lanflow for this session; ask only when the task really needs them.

## Standard flow

1. **Look**: `status` (and `diagnose` when something is wrong). Read names exactly as returned — node and group names are often Chinese with emoji.
2. **Preview**: use `dry_run` / `dryRun` (or `--dry-run`) and show the user the diff for any settings or rule change that is not a plain node switch.
3. **Apply**: make the smallest change that does the job. The first write from you makes Lanflow ask the user (just this time / always / deny); if denied, stop and tell the user — do not retry or look for another route.
4. **Verify**: `test_rule` for routing, `test_proxy_delay` / `switch_node` for nodes, `diagnose_site` or `test_sites` for reachability. Report what changed and that `undo` reverts it.

## Common requests

- **"Make X go through node/group Y"** → `proxies` to find the exact name → `route_domain {domain, target, dry_run: true}` → show the diff → `route_domain {domain, target}` → report `verified`, the exit node and its delay. Use `rule_type: DOMAIN` for one exact host, the default DOMAIN-SUFFIX for a site and its subdomains. To send a site direct, target `DIRECT`.
- **"Switch to a faster node" / "the current node is slow"** → `switch_node {group}` (empty `node` picks the fastest); name a specific node only if the user asked for it.
- **"Update my subscription"** → `update_subscription` (current one) or `update_all_subscriptions`; then `subscriptions` to report `UpdatedAt` and `LastError`. Never ask for or print the subscription link.
- **"X does not load"** → `diagnose_site {host}`; act on its findings: core stopped → `start_core`; neither system proxy nor TUN on → offer `set_system_proxy`; exit node fails → `switch_node`; wrong route → `route_domain`; DNS empty → check `effective_dns` and `dns_query`.
- **DNS leak** → `run_leak_check` then `leak_check`. If DNS leaks while the system proxy is used, the usual fix is TUN (`set_tun true`; needs the privileged helper — if it is missing, tell the user to install it in Lanflow) or letting Lanflow take over DNS: `patch_settings {"DNS":{"TakeOver":true}, dryRun:true}`, show the diff, apply, re-run the leak check. For IPv6 leaks, report the finding's advice; do not disable IPv6 system-wide.
- **Mode** → `set_mode rule` is the normal mode; `global` sends everything through the main group, `direct` turns the proxy off for all traffic. Say which one you set.

## Never

- Never export, reveal or ask for subscription links or node credentials (`subscription_raw`, `runtime_config` only when the user explicitly needs them and approves).
- Never turn off safety checks: do not weaken leak protection, aTrust compatibility, LAN authentication or strict routing to "make it work".
- Never touch the privileged helper, the root certificate or HTTPS decryption, the core binary, backups or WebDAV credentials — these are not offered to agents; send the user to Lanflow's settings instead.
- Never delete subscriptions or reset statistics. Do not loop retries after a refusal; one clear explanation is better.
