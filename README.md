# time_sync · 自动校时工具（Go 版 v1.0.0）

纯 Go 实现的 NTP/HTTP 校时工具，单文件 exe，无任何运行时依赖。内置 **Web 控制面板**（浏览器打开即用）。

- 语言：Go 1.22（Windows / Linux / macOS 通用，已用 Windows amd64 编译）
- 依赖：`golang.org/x/sys`（系统调用），其余为标准库
- 产物：`timesync.exe` 单文件（约 9 MB，无需安装）
- Web 面板：默认启动并打开浏览器（`http://127.0.0.1:8765/`），可用 `--web`/`--no-open`/`--menu` 切换

## 快速开始

```
timesync.exe              # 双击：起 Web 面板并打开浏览器
timesync.exe --query     # 仅查询偏差（不改系统时间）
timesync.exe --sync      # 立即校正（需管理员）
timesync.exe --menu      # 交互菜单（0-12 + w 打开面板）
timesync.exe --help      # 全部参数
```

## 功能

- **NTP 并发查询**：同时打多个 NTP 源，剔除离群值后取延迟最低者，精度毫秒级。
- **HTTP 兜底**：NTP 全失败（UDP 123 被封）时改用 HTTP `Date` 头，秒级精度。
- **偏差阈值**：偏差小于阈值不写时钟，避免无谓改动（`--threshold` / 配置）。
- **循环校时**：`--loop --interval 60` 每 N 分钟校准一次（或 `--install-scheduler` 注册系统计划任务）。
- **日志 + 漂移报告**：结构化日志可估算本机晶振漂移 ppm（`--drift-report` 或面板内按钮）。日志默认写在 exe 同目录 `校正日志.txt`，开箱即记录。
- **回拨确认**：校正需把时钟回拨超过 60 秒时，交互场景会提示确认并说明影响；面板路径改为先弹窗告知回拨幅度，不会静默倒退时钟。
- **冲突提醒**：系统时间服务 `w32time` 运行时会提示可能覆盖校正结果。
- **平滑校时（可选）**：`slew = true` 时把偏差拆成多个小步进逼近，避免时钟突跳（默认关闭，一次性写入）。
- **Windows 集成**：计划任务、注册表开机自启、w32time 服务开关，均带自动 UAC 提权。
- **Web 控制面板**：状态卡 + 立即校准 + 改服务器 + 集成开关 + 实时日志流 + 漂移报告。

## 命令行参数

```
查询/校时：
  --query              查询时间偏差（不修改系统时间）
  --sync [--force]     立即校正系统时间
  --loop --interval N  循环校时（分钟）
  --allow-back         允许把系统时钟往回拨
  --quiet              静默（适合计划任务/脚本，配合退出码判断）
  --diag               自检：环境信息 + w32time 状态 + 各时间源连通性（不校时）

时间源：
  --server <host>      临时指定 NTP 服务器（可多次，覆盖配置）
  --http <url>         临时指定 HTTP 兜底源（可多次）
  --set-server <list>  把 NTP 列表(逗号分隔)写入 配置.ini 并退出
  --set-http <list>    把 HTTP 列表(逗号分隔)写入 配置.ini 并退出

Web 面板：
  --web [-listen IP:PORT] [-no-open]   启动面板并打开浏览器

集成（Windows，管理员）：
  --install-scheduler [-scheduler-interval 60]   注册计划任务（每小时）
  --remove-scheduler                               删除计划任务
  --install-startup / --remove-startup            注册表开机自启
                      加 --machine 写 HKLM（所有用户），默认 HKCU（当前用户）
  --disable-w32time / --enable-w32time            停用/启用系统时间服务

通用：
  --config <path>     指定配置文件
  --no-config         忽略配置文件，仅用命令行参数与内置默认值
  --log <path>        日志文件
  --no-log            本次运行不写日志
  --json              JSON 输出（自检/脚本解析用）
  --report-limit <n>  漂移报告展示条数（默认 20）
  --timeout <sec>     单源超时（默认 5）
  --threshold <sec>   偏差阈值（默认 0.5）
  --retries <n>       每源重试次数（默认 2）
  --version / --help  版本 / 帮助
```

## 退出码（脚本/计划任务可用）

| 码 | 含义 |
|----|------|
| 0  | 成功 |
| 1  | 失败（无可用时间源 / 无权限 / 写时钟失败） |
| 2  | 跳过（偏差 < 阈值，无需校正） |

## 配置文件 `配置.ini`

放在 `timesync.exe` 同目录自动加载。改 NTP/HTTP 服务器、阈值、日志路径在此文件改即可，无需重编译。

```
[servers]
ntp  = ntp.aliyun.com, time.windows.com, ...
http = https://www.baidu.com, https://www.qq.com, ...

[sync]
timeout   = 5
threshold = 0.5
force     = false
interval  = 60
retries   = 2
log       = 校正日志.txt   # 相对路径按 exe 所在目录解析；留空=不记录
slew      = false          # 平滑校时（分段逼近），默认关闭=一次性写入
slew_max  = 60             # 平滑校时适用的最大偏差（秒）
```

> `log` 的默认值就是 `校正日志.txt`，且相对路径按 **exe 所在目录** 解析（不是当前工作目录），
> 因此计划任务运行时（CWD 常为 `System32`）日志也不会写飞。

## 与 Python 版（v1.3.2）的差异

- 无 PyInstaller / 无 Python 运行时，单文件更小、启动更快。
- 新增 Web 控制面板（内嵌 HTML，非独立文件）。
- 提权 preselect 改用环境变量 `TIMESYNC_PRESELECT`（语义一致：菜单选管理员功能后提权进程直接执行该项，无需重选）。
- 其余 CLI 参数、退出码、配置格式、NTP/HTTP 算法、日志/漂移报告均保持一致。

## 图标与版本信息

exe 内嵌图标（蓝渐变表盘，多尺寸 16/24/32/48/64/128/256）与 Windows 版本资源，
资源管理器显示图标、右键「属性 → 详细信息」可见：

| 字段 | 值 |
|---|---|
| 产品名称 | time_sync 自动校时工具 |
| 文件说明 | 自动校时工具（NTP + HTTP 兜底） |
| 文件/产品版本 | 1.0.0 |
| 版权 | MIT License |

资源由 `rsrc_windows_amd64.syso` 提供（`go build` 自动链接）。图标源在 `icon.ico`，
可用 `tools/makeicon` 重新生成：`go run ./tools/makeicon icon.png icon.ico`。

> manifest 为 `asInvoker`（**按需提权**）：双击不弹 UAC，只有选管理员功能时才 `runas`。
> 若想改成像部分工具那样「双击即提权」，把 `publish_go.py` 里 `--manifest cli` 改成
> `--manifest cli --admin` 重新打包即可（代价：每次双击都弹 UAC）。

## 构建

```
# 需要 Go 1.22+（已用 goproxy.cn 拉取 golang.org/x/sys@v0.20.0，需 GOTOOLCHAIN=local 避免自动升 1.26）
go build -ldflags "-s -w" -o timesync.exe .
# 可选：去控制台窗口（Web 模式更干净，但 --query 等 CLI 输出不可见）
#   -ldflags "-H windowsgui -s -w"
```

重新打包分享包：`python publish_go.py`（自动生成图标/版本资源 → 产出
`自动校时工具_Go_分享包.zip` 与 `自动校时工具_Go_自解压.exe`）。
需要 `go-winres`（`go install github.com/tc-hib/go-winres@latest`）；缺失时降级跳过图标/版本，不影响构建。
