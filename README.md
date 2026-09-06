# mihomoctl

`mihomoctl` 是面向 Linux 的 Mihomo 命令行前端，提供紧凑的中文 TUI 和适合脚本调用的 CLI。它复用系统中已有的 Mihomo 与 systemd 服务，不安装、不升级 Mihomo 内核。

## 功能

- 总览、节点、配置、连接、日志、设置六个 TUI 页面。
- Mihomo 服务、开机启动、运行模式、TUN 和监听网络设置管理。
- 策略组切换、节点测速、连接查看与关闭、实时日志。
- 远程 Clash/Mihomo YAML、本地 YAML、节点 URI 和 Base64 URI 订阅。
- 支持 `ss`、`vmess`、`vless`、`trojan`、`hysteria2`/`hy2`、`tuic` 节点 URI。
- 订阅条件更新、流量信息、24 小时默认更新周期和可选 systemd timer。
- 配置应用前执行 `mihomo -t`；原子替换、健康检查和失败回滚。
- 表格与稳定的 JSON/JSON Lines 输出，支持 Shell 补全和 `NO_COLOR`。

## 系统要求

- 使用 systemd 的 Linux，系统已安装并配置 Mihomo `1.19.x` 或更新版本。
- 已有可探测的 Mihomo service，默认名称为 `mihomo.service`。
- 发布包支持 Linux `amd64` 和 `arm64`，二进制不依赖 CGO。
- TUI 终端至少为 `60x16`；不要求 Nerd Font。
- 从源码构建需要 Go 1.27。

## 安装

发布页提供 tar.gz、deb、rpm、SHA256 校验文件和 SBOM。安装软件包不会自动启用或启动订阅更新 timer。

Debian/Ubuntu：

```bash
sudo apt install ./mihomoctl_0.1.0_linux_amd64.deb
```

Fedora/RHEL：

```bash
sudo dnf install ./mihomoctl_0.1.0_linux_amd64.rpm
```

tar.gz：

```bash
tar -xzf mihomoctl_0.1.0_linux_amd64.tar.gz
sudo install -d -m 0755 /usr/local/bin /usr/local/lib/systemd/system
sudo install -m 0755 mihomoctl /usr/local/bin/mihomoctl
sudo install -m 0644 packaging/systemd/mihomoctl-update.{service,timer} /usr/local/lib/systemd/system/
sudo systemctl daemon-reload
```

使用 `arm64` 机器时，将文件名中的架构替换为 `arm64`。安装后可用 `mihomoctl version` 检查版本。

## 首次初始化

先确认 Mihomo 可以由 systemd 管理：

```bash
mihomo -v
systemctl status mihomo.service
mihomoctl doctor
```

然后接管现有配置：

```bash
mihomoctl init
```

`init` 会按需调用 `sudo`，探测 Mihomo 二进制、service 和 `ExecStart` 中的 `-d`/`-f` 路径，将现有配置导入为不可变的“系统原配置”恢复快照，并默认创建仅监听 `127.0.0.1:9090` 的带密钥控制器。该快照不会被 `profile update --all` 或定时更新覆盖。应用配置前会先校验；运行中的 Mihomo 会被重启并接受健康检查，失败时恢复备份。

探测结果不符合实际环境时可以明确指定：

```bash
mihomoctl init \
  --service mihomo.service \
  --config /etc/mihomo/config.yaml \
  --controller 127.0.0.1:9090
```

`--controller` 只接受回环地址。`--force` 用于重新初始化现有 mihomoctl 状态，使用前应先检查备份。

## 常用操作

在交互式终端中不带参数启动 TUI；stdin/stdout 被重定向时会显示帮助而不会占用全屏：

```bash
mihomoctl
```

常用 CLI：

```bash
mihomoctl status
mihomoctl service restart
mihomoctl service enable --now
mihomoctl mode rule
mihomoctl tun on

mihomoctl proxy list
mihomoctl proxy select PROXY "节点名称"
mihomoctl proxy test PROXY

mihomoctl profile list
mihomoctl profile add https://example.com/subscription --name 主订阅
mihomoctl profile add ./config.yaml --name 本地配置
mihomoctl profile update --all
mihomoctl profile use 主订阅

mihomoctl connections list
mihomoctl connections close --all
mihomoctl logs --follow --level info
```

自动更新默认关闭。显式启用后，timer 每 15 分钟检查一次到期订阅，并增加最多 5 分钟的随机延迟：

```bash
mihomoctl schedule enable
mihomoctl schedule status
```

状态、配置、节点、连接等非流式业务命令可使用 `--output json`；日志的 JSON 模式按行输出独立 JSON 对象：

```bash
mihomoctl status --output json
mihomoctl logs --follow --output json
```

脚本可依赖退出码：`0` 成功、`1` 操作失败、`2` 输入无效、`3` 权限失败、`4` Mihomo 或控制器不可用。

受管配置只允许修改以下项目：

```bash
mihomoctl config set mixed-port 7890
mihomoctl config set allow-lan off
mihomoctl config set ipv6 off
mihomoctl config set log-level info
```

生成补全脚本：

```bash
mihomoctl completion bash
mihomoctl completion zsh
mihomoctl completion fish
```

## TUI 快捷键

| 按键 | 操作 |
| --- | --- |
| `Tab` / `Shift+Tab`、`h` / `l` | 切换页面 |
| `j` / `k`、上下方向键 | 移动选择或逐行浏览日志 |
| `Home` / `End`、`PgUp` / `PgDn` | 跳到首尾或整页翻动长列表与日志 |
| 节点页左右方向键、`[` / `]` | 切换策略组 |
| `Enter` | 执行当前操作 |
| `/` | 筛选节点、连接或日志 |
| `t` | 测试当前策略组 |
| `a` / `u` / `d` | 添加、更新、删除配置 |
| `Space` | 暂停或继续日志自动滚动；暂停期间仍保留新日志 |
| `r` | 刷新 |
| `?` | 显示帮助 |
| `q` / `Ctrl+C` | 退出 |

删除配置、关闭连接、关闭全部连接和停止服务会要求确认。

### 设置页

设置页包含服务、开机启动、运行模式、TUN、定时更新、混合端口、允许局域网、IPv6 和日志级别九项。使用上下方向键选择，按 `Enter` 修改；长列表会自动滚动并保持当前项可见。

- 混合端口使用预填输入框，只接受 `1` 到 `65535`；无效输入会留在输入框中并显示错误。
- 日志级别通过单选列表设置，可选 `debug`、`info`、`warning`、`error` 和 `silent`。
- 开启“允许局域网”前必须再次确认，因为代理端口会对同一局域网开放；Mihomo 控制器始终只监听回环地址，不会随之暴露。
- 每项在确认后立即保存。Mihomo 正在运行时，配置会先通过内核校验，再重启服务并执行健康检查；失败会恢复上一份可用配置。服务已停止时只保存设置，不会自行启动服务。

## 权限与敏感信息

- 状态、节点、测速、连接和日志通过本地 Mihomo API 完成，正常情况下不需要提权。
- 初始化、配置/订阅变更、TUN、systemd 服务和 timer 操作会按需通过 `sudo` 重执行；不会经过 shell。
- `/etc/mihomoctl` 权限为 `0700`。`/var/lib/mihomoctl` 为可穿越的 `0755`，其中只在根目录放置脱敏的 `0644 public.json`；`store/`、`backups/` 和敏感 YAML 仍限制为 `0700`/`0600`。
- `~/.config/mihomoctl/client.yaml` 权限为 `0600`，其中含本地控制器密钥。不要共享该文件，也不要加入版本控制。
- mihomoctl 自身产生的订阅错误与配置列表会隐藏 URL token、节点密码、UUID 和控制器密钥。
- TUI 添加配置时会遮蔽输入的订阅 URL、节点 URI 或本地路径；筛选输入仍正常显示。
- `logs` 内容来自 Mihomo 内核，可能包含目标地址或进程等运行信息；对外分享前仍应检查并脱敏。
- 带 token 的订阅 URL 直接写在命令行中可能进入 Shell 历史。可使用 `mihomoctl profile add - --name 私有订阅`，从标准输入粘贴内容后按 `Ctrl+D`。
- 下载超时默认为 30 秒，单份订阅上限为 10 MiB；无效更新不会替换最后可用配置。
- `allow-lan` 只控制 Mihomo 代理端口是否对局域网开放；控制器仍被强制绑定到回环地址。

主要状态路径：

| 路径 | 内容 |
| --- | --- |
| `/etc/mihomoctl/config.yaml` | root-only 管理状态与控制器密钥 |
| `/var/lib/mihomoctl/public.json` | 不含来源、验证器和密钥的配置元数据及当前监听网络设置，供普通用户读取 |
| `/var/lib/mihomoctl/store` | 配置和订阅快照 |
| `/var/lib/mihomoctl/backups` | 应用前的 Mihomo 配置备份 |
| `~/.config/mihomoctl/client.yaml` | 当前用户访问本地控制器所需的信息 |

升级安装会尽力从当前活动配置同步 `public.json` 中的设置快照，因此 Mihomo 停止时 TUI 仍可显示最近一次成功应用的值。旧版快照在同步完成前会显示“未知”；后续成功修改、切换或更新配置时也会自动补齐。公开快照不会写入订阅地址、节点凭据或控制器密钥。

## 安全卸载

卸载前先停用自动更新；软件包的卸载脚本也会在真正删除软件包时执行此操作：

```bash
sudo mihomoctl schedule disable
sudo apt remove mihomoctl       # Debian/Ubuntu
# 或：sudo dnf remove mihomoctl # Fedora/RHEL
```

源码安装默认位于 `/usr`，可这样移除程序和 timer：

```bash
sudo systemctl disable --now mihomoctl-update.timer
sudo systemctl stop mihomoctl-update.service
sudo rm -f /usr/bin/mihomoctl
sudo rm -f /usr/lib/systemd/system/mihomoctl-update.service
sudo rm -f /usr/lib/systemd/system/mihomoctl-update.timer
sudo rm -f /usr/share/doc/mihomoctl/LICENSE
sudo rm -f /usr/share/doc/mihomoctl/README.md
sudo rmdir /usr/share/doc/mihomoctl 2>/dev/null || true
sudo systemctl daemon-reload
```

使用 `--prefix /usr/local` 安装时，将上述 `/usr` 替换为 `/usr/local`。卸载默认保留 `/etc/mihomoctl`、`/var/lib/mihomoctl`、用户 client 文件和 Mihomo 配置；确认 Mihomo 已能独立运行且备份不再需要后，再手动清理这些数据。删除数据不可恢复。

## 从源码构建

```bash
make test
make build
./dist/mihomoctl version
```

安装当前源码：

```bash
./scripts/install.sh
```

脚本先以当前用户构建，再只对安装步骤使用 `sudo`。可使用 `--prefix /usr/local`，或使用 `--no-units` 只安装二进制。`DESTDIR` 可供打包和无副作用的安装测试使用。

开发检查与本地发行快照：

```bash
make check
make test-race
make snapshot
```

`make snapshot` 需要 GoReleaser v2.10 或更新版本及 Syft，产物写入 `dist/`。

## 许可证

[MIT](LICENSE)
