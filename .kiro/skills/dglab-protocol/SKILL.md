---
name: dglab-protocol
description: DG-LAB 郊狼 Socket 协议参考（3.0 APP 的 V3 socket 与 4.0 APP 的 V4 协议）及官方 SDK 来源。修改 internal/device/（v3.go、v4.go、hub.go）、强度/波形/屏蔽/设备状态相关逻辑，或用户问"APP/协议支不支持 xxx"时使用。
---

# DG-LAB Socket 协议参考

先查官方来源再下结论，不要凭 Python 版或旧常量猜协议。下面是 2026-11-12 核对过的结论。

## 官方来源

| 内容 | 仓库 | 说明 |
|---|---|---|
| V4 协议 + TS SDK | `github.com/dungeonlab-open/dglab-kit`（README_CN.md、`src/socket/v4/types.ts`） | 权威。V3 也有封装 |
| V4 中继服务端 | `github.com/dungeonlab-open/dglab-websocket-server` | 官方 relay |
| 设备 WebSocket 简例 | `github.com/dungeonlab-open/dglab-websocket-simple` | |
| V3 socket 文档 | `github.com/DG-LAB-OPENSOURCE/DG-LAB-OPENSOURCE` | `socket/` 已在提交 `0dfae76` 删除，用 `git show 0dfae76^:socket/v2/README.md` 查看 |
| 郊狼蓝牙协议 | 同上仓库 `coyote/v3/README_CN.md` | 波形 B0 指令、强度 >100 的帧被丢弃 |

查证方法：`git clone --depth 1` 到 /tmp，`grep -rn`；查删除的能力用 `git log -S"<关键字>" --all` 看历史。用完删除 /tmp 下的克隆。
GitHub 未登录的代码搜索 API 不可用，仓库搜索可用：`curl -s "https://api.github.com/search/repositories?q=dglab+relay"`。

## V3（DG-LAB 3.0 APP「Socket 控制」，本项目 WS 端口 28846）

- 帧：`{"type","clientId","targetId","message"}`。我们是服务端 + 控制端：先发 `bind`（message=`targetId`），APP 回 `bind` 后应答 `200`。
- 二维码：`https://www.dungeon-lab.com/app-download.php#DGLAB-SOCKET#ws://<host>:<port>/<master_uuid>`（`App.QRContent`）。
- 控制端 → APP（`type:"msg"`）：
  - `strength-<通道1|2>+<模式0减|1加|2设为>+<值>`，值 0~200
  - `pulse-<A|B>:["8字节HEX",...]`，每条 100ms，单次 ≤100 条，APP 队列 ≤500 条
  - `clear-<1|2>` 清空波形队列
- APP → 控制端：`strength-A+B+A上限+B上限`（强度或上限变化时上报）、`feedback-<0..9>`（反馈按钮）
- **没有屏蔽/静音指令，也不上报屏蔽状态。** 心跳 `type:"heartbeat"`。
- APP 只在收到强度指令后才上报强度，所以 `v3.go` 在连接 2 秒后主动下发目标强度。

## V4（DG-LAB 4.0 APP，复用 Web 端口 `/ws/dglab-v4?tid=<ControllerID>`）

- 二维码：`https://dungeon-lab.cn/s/?v=1&action=socket&url=<urlencode(ws://...)>`（`App.QRContentV4`）。
- 我们同时是 relay 和控制端：APP 连入后发 `{"type":"hello","clientId"}`、`{"type":"controller_attached","clientId":<ControllerID>}`；业务消息包在 `{"type":"message","data":{...}}`。
- RPC：`{"t":"req","reqId","m","data"}` / `{"t":"resp","reqId","result"|"error"}`；事件 `{"t":"ev","ev":...}`。
- 方法：`devices.get`、`ping`（结果是 APP 本地时间戳，RTT 自己算）、`device.op`、`device.op.clear`（`{}` / `{s}` / `{s,c}`，只传 `c` 报 `invalid_params`）。
- `device.op` 公共字段：`s` slotId，`t` ActionType，`c` 0=A 1=B，`p` 优先级 0|1|2（默认 1），`d` 持续 ms，`im` 替换同类任务。
- **公开 ActionType 只有 4 种**：

| t | 名称 | 说明 |
|---|---|---|
| 0 | AppendPulseData | `v` 帧列表（HEX 字符串或数组），`ver` 2/3，持续任务 |
| 3 | AddIntensity | `v` 正负相对值，oneShot |
| 4 | SetTempIntensity | 临时强度，任务结束 APP 自动归 0 |
| 7 | SetIntensity | **只能设为 0**，其他值不接受 |

  所以"设为 N"只能 AddIntensity(N - 当前值)，见 `V4.SetStrength`。
- `device.op` 在任务**结束**时才响应（`reason`: completed/cleared/replaced/cancelled）；错误：`duplicate_request_id`、`invalid_params`、`slot_not_found`、`invalid_operate`、`method_not_found`、`internal_error`。
- 事件：
  - `devices.snapshot`：连接后立即发送全部设备（可能为空数组），`props` / `slotState` 为完整快照
  - `devices.patch`：`added[]`（完整）/ `removed[]`（slotId）
  - `slots.patch`：`slots[]` 的 `props` / `slotState` 是**增量**，按 slotId 深合并
  - `custom.action`：`action` 为 0..9
- 郊狼 3.0（`COYOTE_030`）字段：`props.intensityA/B`；`slotState.channelA/B.{isMuted, warmUpScale(冷启动系数 0~1), intensityMax, comfortLimit{mode, comfortMax, absoluteMax, overheat, autoIncr...}}`。

### 屏蔽（静音）

- `SetMute`（t=5，`v: bool`）曾在 SDK 中，官方提交 `05ebc5f`（2026-06-26，"去除失效的屏蔽通道协议"）删除。**现在不能远程开关屏蔽**，发送 t=5 预期返回 `invalid_operate`。
- `isMuted` 只能读取。`v4.go` 里的 `ActionSetMute = 5` 是 Python 版遗留常量，不要当作可用接口。
- 用户反馈"连上后 APP 输出是关的"：先看日志 `[v4] Device ... snapshot:` 中的 `isMuted`。
- 0.7.23 起：`V4.updateMuted` 读取 `isMuted`（snapshot 为完整状态，patch 只看出现的字段），存入 `Status.AppMuted`（`app_muted`），仪表盘提示，日志记 WARN。
- 实验开关 `ws.v4_try_unmute`（默认 false，设置页 V4 下方）：通道被屏蔽时每个 snapshot 发送一次 `device.op {t:5, v:false}`，APP 的响应或错误以 INFO/WARN 记入日志（`unmuteReqs`）。测试：`TestV4AppMuted`。

## 本项目的强度模型（0.7.22 起）

- 程序为准：目标 = 输出关闭时为 0，否则 min(上限 + 超限加成, 200)，再受 APP `intensityMax` 限制（`chanState.upperLocked`）。
- `Hub.reconcile`：APP 上报与目标不一致就设回；首次上报的 0 视为"未设置"；**用户在 APP 里把有强度的通道降到 0**（距我们上次下发 >1.5s）视为急停，调用 `Hub.SetOutput(ch, false, "app")`。
- 输出开关：`POST /api/v1/output {channel, on}`；`/api/v1/strength/.../set/0` 等同于关闭。
- 相关测试：`TestOutputSwitchV3`、`TestV4RelayHandshake`、`TestEndToEndV3DeviceAndOSC`。
