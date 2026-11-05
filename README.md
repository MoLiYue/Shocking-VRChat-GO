# Shocking VRChat — Go + WebView2 版

[Shocking-VRChat](https://github.com/VRChatNext/Shocking-VRChat)（Python / FastAPI）的 Go 重写版。
接收 VRChat Avatar 的 OSC 参数，通过 DG-LAB APP 的 WebSocket 协议驱动郊狼 3.0。
管理界面沿用原项目的 Vue 前端，在 Windows 上以原生 WebView2 窗口显示。

> [!CAUTION]
> 使用前必须阅读并同意 [安全须知](doc/dglab/安全须知.md)（[Safety Precautions](doc/dglab/SafetyPrecautions.md)）。

## 与 Python 版的差异

| 项目 | Python 版 | Go 版 |
|---|---|---|
| 运行时 | Python + PyInstaller 目录（约 60 MB） | 单个 `shocking_vrchat.exe`（约 10 MB），无需 Python |
| 界面 | 托盘图标 + 系统浏览器 | WebView2 原生窗口；关闭窗口即退出程序 |
| 前端 / 模板 / 默认波形 | 外部文件 | 编译进 exe；首次运行把默认 `wave_presets/`、`dg-lab/` 解压到 exe 旁 |
| 配置文件 | `settings-v0.2.yaml` 等 | 完全相同，可直接沿用旧配置 |
| HTTP API / WebSocket | `/api/v1/*`、`/ws/live`、`/ws/dglab-v4` | 路由与 JSON 结构保持一致，前端无需修改 |
| 自动更新 | 下载 `windows_x64.zip` | 下载 Release 中以 `go_windows_x64.zip` 结尾的资源 |

功能全部移植：distance / shock / touch / combo / boost 模式、强度曲线、超限规则、SPS2/OGB 深度、
波形预设导入（.pulse/.json）与预览、波形测试、OSC 录制回放、Chatbox、配置档案、配置导入导出、热重载、V3 + V4 设备协议。

## 运行（Windows）

1. 下载并解压 `shocking_vrchat_go_windows_x64.zip`，运行 `shocking_vrchat.exe`。
2. 首次运行生成默认配置；Windows 防火墙弹窗请选择「允许」（手机需要访问 WS 端口 28846 / Web 端口 8800）。
3. 用 DG-LAB APP「Socket 控制」扫描界面中的二维码。

Windows 10/11 已自带 WebView2 运行时；若缺失，程序会自动改用系统浏览器打开管理页面，
也可从 <https://go.microsoft.com/fwlink/p/?LinkId=2124703> 安装。

命令行参数：

| 参数 | 说明 |
|---|---|
| `-browser` | 不用 WebView2 窗口，改用系统浏览器 |
| `-headless` | 不打开任何界面（仅后台服务，通过 `/api/v1/shutdown` 或 Ctrl+C 退出） |
| `-dir <path>` | 数据目录（配置、预设、日志），默认 exe 所在目录 |
| `-devtools` | 启用 WebView2 开发者工具 |

日志写入 `shocking_vrchat.log`（5 MB 轮转），启动失败写入 `error.log` 并弹窗提示。

## 安全说明

Web 管理接口（默认 `0.0.0.0:8800`）与 DG-LAB WS 服务（`0.0.0.0:28846`）**没有密码**，与原版一致：
同一局域网内的设备可以直接访问管理页面和 API。V4 中继需要匹配随机 controller id，V3 绑定需要匹配 `master_uuid`。
如不需要手机以外的设备访问管理页面，可把 Web 监听地址改为 `127.0.0.1`
（注意 V4 协议复用 Web 端口，改为本机地址后 DG-LAB 4 APP 将无法连接）。

针对浏览器的防护（Go 版新增）：

- **跨站请求拦截**：其他网站的页面不能调用 API 或连接 WebSocket（例如用 `<img>` 触发电击），
  依据浏览器的 `Sec-Fetch-Site` / `Origin` 判断。DG-LAB APP、脚本、curl 等非浏览器客户端不受影响。
- **DNS 重绑定防护**：只接受 IP、`localhost` 和本机名作为 Host。如果通过自定义域名访问管理页面，
  需在 `settings-advanced-v0.2.yaml` 的 `web_server.allowed_hosts` 中添加该域名。
- 以上两项只作用于管理页面和 API；设备端点（WS 端口 28846、`/ws/dglab-v4`）不做 Origin/Host 检查，
  只校验二维码里的随机 `master_uuid` / `tid`，避免 DG-LAB APP 连接被拒。
- **强度接口受上限约束**：`/api/v1/strength/.../set|add` 与 `strength_boost` 不再绕过强度上限。

## 构建

依赖：Go ≥ 1.26、Node.js ≥ 18（仅修改前端时需要）。无需 CGO，可在 Linux 上交叉编译 Windows 版本。

```bash
./build.sh            # 前端 + 测试 + dist/shocking_vrchat_go_windows_x64/ 及 zip
SKIP_FRONTEND=1 ./build.sh   # 复用已提交的 assets/static
./build.sh linux      # 本机版本（无 WebView，使用浏览器）
```

Windows 上可用 `build.ps1`。仓库已提交前端构建产物 `assets/static/` 与 `assets/bundled.zip`，
所以直接 `go build .` 也能编译；修改前端或波形后重新运行构建脚本即可刷新。

开发时：

```bash
go run . -browser                 # 后端
cd frontend && npm run dev        # 前端热更新（代理到 :8800）
go test -race ./...
```

## 代码结构

```
main.go                 入口：加载配置、启动 Web/引擎、打开 WebView2 窗口
assets/                 嵌入的前端构建、旧版 HTML 模板、默认波形 zip
internal/config         配置树（与 YAML 结构一致）、默认值、热重载 mtime
internal/logx           类 loguru 日志：控制台 / 轮转文件 / Web 日志缓冲
internal/wave           波形 op 工具、预设库、.pulse 转换器
internal/sps            SPS1/SPS2(OGB) 深度聚合
internal/queue          带冷却的优先级命令队列
internal/osc            OSC 编解码、UDP 服务端（python-osc 兼容通配符）与客户端
internal/device         DG-LAB V3 Socket 服务、V4 中继、设备广播
internal/handler        ShockHandler：各模式输入处理与 100ms 波形 feeder
internal/app            引擎、全部 HTTP API、/ws/live、录制回放、Chatbox、更新器
internal/ui             WebView2 窗口（Windows）/ 浏览器回退
tools/packassets        把 wave_presets、dg-lab 打包为 assets/bundled.zip
winres/                 exe 图标、DPI 清单、版本信息
```

测试覆盖：所有内置 `.pulse` 的转换结果与 Python 版生成的预设逐 op 比对；波形窗口重采样、SPS 深度与
Python 输出一致；端到端测试用模拟 DG-LAB APP（V3 绑定 / 强度跟随 / pulse / clear）、
V4 中继握手、真实 UDP OSC 输入及主要 API。

## License

AGPL-3.0，同原项目。波形文件声明见原项目 README。
