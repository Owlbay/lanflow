# 澜流搭建方案

## 当前状态

已生成 MyGo v0.8.3 原生 UI 模板，并使用澜流 / Lanflow 名称。以下模块均为后续实现计划，不代表功能已经可用。尚未下载、打包或运行 Mihomo。

## 分层

```text
MyGo 原生 UI / 托盘
        ↓
应用服务：配置、节点、订阅、运行状态
        ↓
Mihomo 进程管理器 + 本机 API 客户端
        ↓
Mihomo 子进程：代理协议、路由规则、DNS

系统代理适配器 / 后续 TUN 权限助手独立于上述层次
```

计划目录：

| 目录 | 职责 |
| --- | --- |
| internal/app | 应用服务与状态，UI 不直接执行内核命令 |
| internal/core | Mihomo 生命周期、版本校验与退出恢复 |
| internal/controller | REST、WebSocket、鉴权、超时与取消 |
| internal/profile | 原始配置、订阅、配置合成与校验 |
| internal/platform | 分平台系统代理、开机启动、权限助手 |
| internal/view | MyGo 页面与组件 |
| resources | 图标与打包资源 |
| bin（忽略） | 开发时的内核二进制 |
| runtime（忽略） | 仅供开发的运行数据 |

按功能创建目录，避免提前堆积空接口。初期设置使用 JSON、代理配置使用 YAML；暂不引入数据库。

## 内核接入

- 从 Mihomo 官方 release 选定并固定版本，记录平台、架构、下载来源和校验值。独立于 MyGo 版本升级。
- 第一步可接入用户指定的本机内核文件；自动下载与随包分发随后实现。
- 启动前检查版本和配置，使用独立工作目录；管理自己启动的 PID，不结束其他代理客户端的进程。
- controller 仅监听 127.0.0.1，生成随机 secret。API 客户端绕开系统代理，设置请求超时，不把 secret 或订阅令牌写进日志。
- 应用覆盖项与订阅原始文件分开保存。订阅不得覆盖本地 controller、secret、监听地址和 TUN 开关。
- 先接 GET /version、GET /proxies 和 PUT /proxies/{name}；再接 /configs、/traffic、/logs、/connections。按官方 API 文档确认方法和请求字段。
- UI 线程不运行阻塞请求；后台请求通过明确的状态更新刷新 UI。退出时取消请求并回收自身子进程。
- 本地配置默认权限 0600，运行目录 0700；正式应用使用系统用户数据目录，避免把订阅和凭据存进代码仓库。

## 分阶段验收

### 1. 框架与内核连通

完成窗口、内核路径设置、版本显示、启动/停止、状态与错误提示。验证启动失败、端口占用、重复启动、内核意外退出和正常退出后的进程清理。

### 2. 最小代理闭环

实现本地配置导入、校验、代理组列表和节点切换。验收：错误 YAML 不替换可用配置；切换后从内核读回结果；不配置系统代理也能显式指定代理端口测试流量。

### 3. 日常使用

实现订阅更新、延迟测试、流量、日志、托盘及系统代理开关。系统代理启用前记录原设置，退出时仅恢复仍由本应用控制的设置；异常退出后提供恢复入口，避免覆盖用户或其他软件的修改。

### 4. TUN 与发行

单独设计最小权限助手、权限申请和撤销、DNS/路由恢复。验证睡眠唤醒、网络切换及崩溃恢复后，再做多平台安装包、签名、公证和更新回滚。

## 资源占用

- 窗口不在前台时不做装饰性动画：地球的空闲摆动与「你」的脉冲只在窗口激活且有流量经过时播放；请求动画在前台 60 fps、后台 30 fps，没有流量的路由不画。经纬网与陆地点阵画进两张 `ui.Canvas`，只在视角变化时重绘（`internal/view/overview/globe_ground.go`）。
- 数据变化触发的重绘有节流：前台每秒至多 4 帧、非激活窗口每秒 1 帧（`view/shell.go` `redrawData`）；窗口隐藏或最小化时服务层进入后台模式（`Service.SetBackground`）：连接流 3 s 一次、代理与规则 60 s 刷新一次，统计按增量累计不受影响。
- 进程内存：`main_pprof.go` 设 96 MiB 软上限与 GOGC 50；`LANFLOW_PPROF=127.0.0.1:6060` 开启 net/http/pprof（默认关闭）。
- 流量统计落盘为 gzip JSON：当月按天、往月合并为 `YYYY-MM.json.gz`，旧版 `.json` 自动迁移（`internal/app/stats_store.go`）。缓存大小与「清理缓存」在设置 › 数据。度量与前后对比见 `build/perf-report.md`。

## 发布准备

MyGo 为 MIT，Mihomo 仓库声明 GPL-3.0。随包分发内核时需准备相应许可证、版权与源码信息；使用独立进程不应被视为自动免除分发义务。发行前再确定本项目许可证和正式应用标识。

## 官方资料

- [MyGo 入门](https://github.com/egoist/mygo/blob/main/docs/getting-started.md)
- [MyGo 分发](https://github.com/egoist/mygo/blob/main/docs/distribution.md)
- [Mihomo Releases](https://github.com/MetaCubeX/mihomo/releases)
- [Mihomo API](https://wiki.metacubex.one/api/)
- [Mihomo LICENSE](https://github.com/MetaCubeX/mihomo/blob/Meta/LICENSE)
