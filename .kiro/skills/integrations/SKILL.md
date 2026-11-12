---
name: integrations
description: 本项目用到的第三方 SDK / 外部服务（WebView2、TinyGo bluetooth、Pulsoid、HypeRate、VRChat OSC、SteamVR、GitHub Release 更新）的版本、接口要点与限制。修改 internal/app/ 下的 ble_hr.go、pulsoid.go、hyperate.go、steamvr*.go、update.go、monitor.go、internal/ui/，或新增依赖时使用。
---

# 第三方 SDK 与外部服务

新增依赖：固定版本（`go get pkg@vX.Y.Z`），先说明会带进来的间接依赖；可选功能默认关闭，放在「模组」页。

## Go 依赖（go.mod）

| 包 | 版本 | 用途 / 注意 |
|---|---|---|
| `github.com/jchv/go-webview2` | pseudo `20260205…` | Windows 窗口（`internal/ui/ui_windows.go`）。窗口钩子用 `windows.NewCallback` + `unsafe` |
| `github.com/gorilla/websocket` | v1.5.3 | 所有 WS（设备、/ws/live、Pulsoid、HypeRate） |
| `tinygo.org/x/bluetooth` | **v0.17.0** | BLE 心率。Windows 走 WinRT（无 CGO），Linux 走 BlueZ/D-Bus |
| `golang.org/x/sys`、`gopkg.in/yaml.v3` | — | |

## TinyGo bluetooth（`internal/app/ble_hr.go`）

- `DefaultAdapter.Enable()` 只调一次（`bleEnable` 用 `sync.Once` + 10 秒超时）。**没有蓝牙服务的 Linux 上 Enable 会一直阻塞**，所以所有调用都必须有超时，停止时最多等 5 秒。
- 同一时间只能有一个 `Scan`（`bleScanMu`）；用 `StopScan()` 结束扫描。
- 断线回调用 `SetConnectionStateHandler(func(Device, ConnectionState))`，`SetConnectHandler` 已弃用。
- 心率服务 `ServiceUUIDHeartRate`（0x180D），测量特征 `CharacteristicUUIDHeartRateMeasurement`（0x2A37）：flags 第 0 位为 0 时心率是 uint8，为 1 时是 uint16 LE（`parseHRMeasurement`）。
- Apple Watch 本身不广播 0x180D，要装心率广播类 App。真机行为没测过（开发机是 Linux）。

## 心率服务（统一由 `publishHR` 输出）

- 程序内参数统一为 `HeartRate/{HeartRate(0..1), BPM, Connected}`；Pulsoid 额外保留 `Pulsoid/*`。可选发送 VRCOSC 兼容的 `/avatar/parameters/VRCOSC/Heartrate/*` 到 `osc.send_host/send_port`。
- **Pulsoid**：`wss://dev.pulsoid.net/api/v1/data/real_time?access_token=…`，用户自己的 token。**不得冒用 VRCOSC 的 client_id**（用户已拒绝）。
- **HypeRate**：Phoenix channel，`wss://app.hyperate.io/socket/websocket?token=<API key>`。加入 `hr:<sessionID>`（`phx_join`），每 10 秒发一次 `{"topic":"phoenix","event":"heartbeat"}`，接收 `hr_update {hr}`。必须要开发者 API Key，**不得使用 HypeRate 网页挂件里内置的 key**。会话 ID 可从分享链接中提取（`normaliseHypeRateID`）。
- 密钥（`pulsoid.token`、`hyperate.api_key`）登记在 `secretKeys` 里：不会通过接口返回，导出时脱敏，导入或载入配置档时保留原值（`keepSecrets`）。

## VRChat OSC

- 输入默认 9001；发送目标统一用 `osc.send_host/send_port`（`oscSendTarget`）；转发用 `osc.forward`。
- Avatar 的 OSC 配置在 `%USERPROFILE%\AppData\LocalLow\VRChat\VRChat\OSC\<usr>\Avatars\<avtr>.json`（`vrchatOSCDir`，参数监视器自动导入时用）。

## SteamVR 自启动（`steamvr*.go`）

- manifest 写到 `config/shocking_vrchat.vrmanifest`（`is_dashboard_overlay: true`，app_key `moliyue.shocking_vrchat`）。
- 注册到 `<Steam>/config/appconfig.json` 的 `manifest_paths`；自启开关写在 `<Steam>/config/vrappconfig/<app_key>.vrappconfig` 的 `autolaunch`。解析失败的文件绝不覆盖。0.7.21 及以前的旧路径（exe 旁）会自动替换掉。

## 更新（`update.go`）

- 资源名必须以 `go_windows_x64.zip` 结尾，并附 `sha256sums.txt`；由 `.github/workflows/build-go.yml` 在推送 `v*` tag 时生成。
- 更新脚本是隐藏的 bat：robocopy 加 `/R:10 /W:1`，延时用 `ping`（不能用 `timeout`），不能用 `pause`；`config/`、`logs/` 和用户内容目录一律不覆盖；日志写到 `logs\update.log`。

## 数据目录（`internal/paths`）

`config/`（设置、规则、曲线、profiles/、窗口状态、manifest）、`logs/`（10 MB × 10 份轮转，以及 crash.log、error.log、update.log）。旧版放在 exe 旁的文件由 `paths.Migrate` 在启动时移入，不覆盖已有文件。新增配置文件要用 `a.ConfigPath(...)`，并加到 `paths.legacyConfig`（如果旧版也有这个文件）。
