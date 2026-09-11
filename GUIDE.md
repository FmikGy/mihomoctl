# mihomoctl 使用指南

[返回项目首页](README.md)

本文包含完整安装、日常操作和管理说明。首次使用可先阅读首页的[快速开始](README.md#快速开始)。

## 目录

- [系统要求](#系统要求)
- [安装](#安装)
- [首次初始化](#首次初始化)
- [日常使用](#日常使用)
- [界面语言](#界面语言)
- [配置与订阅](#配置与订阅)
- [节点选择与测速](#节点选择与测速)
- [模式与网络设置](#模式与网络设置)
- [定时更新](#定时更新)
- [脚本与命令行](#脚本与命令行)
- [TUI 快捷键](#tui-快捷键)
- [权限与敏感信息](#权限与敏感信息)
- [状态路径与备份恢复](#状态路径与备份恢复)
- [升级与卸载](#升级与卸载)
- [源码构建与开发](#源码构建与开发)

## 系统要求

- 使用 systemd 的 Linux，系统已安装并配置 Mihomo `1.19.x` 或更新版本。
- 已有可探测的 Mihomo service，默认名称为 `mihomo.service`。
- 发布包支持 Linux `amd64` 和 `arm64`，二进制不依赖 CGO。
- TUI 终端至少为 `60x16`；不要求 Nerd Font。
- 普通用户需要具备相应的 sudo 权限，才能执行初始化、配置变更和服务管理。
- 从源码构建需要 Go 1.27。

## 安装

### 1. 先安装 Mihomo

`mihomoctl` 是 Mihomo 的管理前端，不包含 Mihomo 内核，也不会安装或升级内核。安装 `mihomoctl` 前，请先安装 Mihomo，并将其配置为可由 systemd 管理的服务（默认服务名为 `mihomo.service`）。

- Mihomo 仓库：[MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo/tree/Meta)
- Mihomo 预编译版本：[Releases](https://github.com/MetaCubeX/mihomo/releases/latest)

安装完成后先确认命令和服务可用：

```bash
mihomo -v
systemctl status mihomo.service
```

### 2. 安装 mihomoctl

[mihomoctl 发布页](https://github.com/FmikGy/mihomoctl/releases/latest)提供 tar.gz、deb、rpm、SHA256 校验文件和 SBOM。请选择对应系统和架构的安装包：x86_64 使用 `amd64`，aarch64 使用 `arm64`。安装软件包不会自动启用或启动订阅更新 timer。

以下命令在下载目录执行；通配符应只匹配本次要安装的一个文件。

Debian/Ubuntu：

```bash
sudo apt install ./mihomoctl_*.deb
```

Fedora/RHEL：

```bash
sudo dnf install ./mihomoctl_*.rpm
```

tar.gz（建议在空目录中解压）：

```bash
tar -xzf mihomoctl_*_linux_*.tar.gz
sudo install -d -m 0755 /usr/bin /usr/lib/systemd/system /usr/share/doc/mihomoctl
sudo install -m 0755 mihomoctl /usr/bin/mihomoctl
sudo install -m 0644 packaging/systemd/mihomoctl-update.{service,timer} /usr/lib/systemd/system/
sudo install -m 0644 README.md GUIDE.md LICENSE /usr/share/doc/mihomoctl/
sudo systemctl daemon-reload
```

上述 tar.gz 安装路径与附带的 systemd unit 中的 `/usr/bin/mihomoctl` 一致。需要自定义安装前缀时，参阅[源码构建与开发](#源码构建与开发)。安装后可用 `mihomoctl version` 检查版本；随包安装的文档位于 `/usr/share/doc/mihomoctl`。

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

`init` 会按需调用 `sudo`，探测 Mihomo 二进制、service 和 `ExecStart` 中的 `-d`/`-f` 路径，将现有配置导入为不可变的“系统原配置”恢复快照，并默认创建仅监听 `127.0.0.1:9090` 的带密钥控制器。该快照不会被 `profile update --all` 或定时更新覆盖。应用配置前会先校验；运行中的 Mihomo 会被重启并接受健康检查，失败时恢复备份。配置备份默认保留最近 20 份。

探测结果不符合实际环境时，需由管理员在独立的 root 登录环境中明确指定。普通用户的自动提权流程不会接受自定义 service 或 root 配置路径；若管理员通过完整的 `sudo -i` root shell 操作，应先清除继承的调用者标记：

```bash
sudo -i
unset SUDO_UID SUDO_GID
mihomoctl init \
  --service mihomo.service \
  --config /etc/mihomo/config.yaml \
  --controller 127.0.0.1:9090
exit
mihomoctl config sync-client
```

最后一条命令应以日常使用 mihomoctl 的普通用户执行。它只把已初始化的控制器地址和密钥安全同步到该用户自己的客户端配置，不会重新应用 Mihomo 配置。

`--controller` 只接受回环地址。`--force` 用于重新初始化现有 mihomoctl 状态，使用前应先检查备份。

## 日常使用

日常交互使用及 TUI 建议始终以普通用户启动，不要使用 `sudo mihomoctl`。在交互式终端中不带参数启动 TUI；stdin/stdout 被重定向时会显示帮助而不会占用全屏：

```bash
mihomoctl
```

TUI 包含总览、节点、配置、连接、日志、设置六个页面。总览显示流量图表，节点页用于选择策略组和节点，配置页用于添加与切换订阅，设置页用于修改服务和网络选项。

查看状态、连接和日志：

```bash
mihomoctl status
mihomoctl connections list
mihomoctl connections close --all
mihomoctl logs --follow --level info
```

## 界面语言

mihomoctl 默认使用简体中文。TUI 与 CLI 均可切换为英文，偏好保存在当前用户的 `~/.config/mihomoctl/preferences.yaml`，不修改 Mihomo 配置，也不需要重启服务。

在 TUI 中进入“设置”页，选中“语言”并按 `Enter`，然后选择“简体中文”或 `English`。保存成功后界面立即切换。

也可从 CLI 查看或持久修改语言：

```bash
mihomoctl language
mihomoctl language en
mihomoctl language zh
```

只想为一次命令或一次 TUI 会话覆盖语言时使用全局参数：

```bash
mihomoctl --lang en status
mihomoctl --lang en
```

显式 `--lang` 的优先级高于已保存偏好，但不会覆盖偏好文件。节点名、策略组名、配置名以及 Mihomo 原始日志保持原文。

## 配置与订阅

支持远程 Clash/Mihomo YAML、本地 YAML 快照、节点 URI 和 Base64 URI 订阅。节点 URI 支持 `ss`、`vmess`、`vless`、`trojan`、`hysteria2`/`hy2`、`tuic`。

在 TUI 中用 `Tab` 切到“配置”页，按 `a` 输入订阅 URL、本地路径或节点 URI；添加后选中配置，按 `Enter` 激活。`u` 更新当前选中的配置，`d` 删除非活动配置。

```bash
mihomoctl profile list
mihomoctl profile add https://example.com/subscription --name 主订阅
mihomoctl profile add ./config.yaml --name 本地配置
mihomoctl profile update --all
mihomoctl profile use 主订阅
```

普通用户添加本地 YAML 时，mihomoctl 会先以当前用户权限读取文件，再将内容作为不可变快照交给 root 保存；文件路径不会越过提权边界，之后修改原文件也不会自动同步。需要更新时请删除后重新添加。远程订阅和节点 URI 仍按设置的间隔更新。管理员在真正的 root 会话中添加受信任的本地文件时，仍可保留按路径更新的行为。

订阅支持条件更新和提供商返回的流量信息，默认更新周期为 24 小时。自动检查到期订阅需要另外启用[定时更新](#定时更新)。

## 节点选择与测速

在“节点”页用左右方向键或 `[` / `]` 选择顶部的策略组，用上下方向键选择节点，按 `Enter` 切换，按 `t` 测试当前组。离开节点页时使用 `Tab` / `Shift+Tab`，左右方向键在此页用于切换组。

```bash
mihomoctl proxy list
mihomoctl proxy select PROXY "节点名称"
mihomoctl proxy test PROXY
```

将示例中的 `PROXY` 和“节点名称”替换为当前配置中实际存在的名称。自动选择等策略组的行为由 Mihomo 和该组配置决定；需要手动指定节点时，选择支持手动选择的策略组。

## 模式与网络设置

设置页包含服务、开机启动、运行模式、TUN、定时更新、混合端口、允许局域网、IPv6、界面语言和日志级别十项。使用上下方向键选择，按 `Enter` 修改；长列表会自动滚动并保持当前项可见。

- 混合端口使用预填输入框，只接受 `1` 到 `65535`；无效输入会留在输入框中并显示错误。
- 日志级别通过单选列表设置，可选 `debug`、`info`、`warning`、`error` 和 `silent`。
- 开启“允许局域网”前必须再次确认，因为代理端口会对同一局域网开放；Mihomo 控制器始终只监听回环地址，不会随之暴露。
- 每项在确认后立即保存。Mihomo 正在运行时，配置会先通过内核校验，再重启服务并执行健康检查；失败会恢复上一份可用配置。服务已停止时只保存设置，不会自行启动服务。

服务、运行模式和 TUN 的 CLI 示例：

```bash
mihomoctl service restart
mihomoctl service enable --now
mihomoctl mode rule
mihomoctl tun on
```

`config set` 支持以下监听与网络设置：

```bash
mihomoctl config set mixed-port 7890
mihomoctl config set allow-lan off
mihomoctl config set ipv6 off
mihomoctl config set log-level info
```

## 定时更新

自动更新默认关闭。显式启用后，timer 每 15 分钟检查一次到期订阅，并增加最多 5 分钟的随机延迟：

```bash
mihomoctl schedule enable
mihomoctl schedule status
```

定时任务使用加固后的 systemd 沙箱，因此 mihomoctl 自身必须使用默认的 `/etc/mihomoctl`、`/var/lib/mihomoctl` 受管路径，活动 Mihomo 配置也不能位于 `/home`、`/root`、`/usr` 或临时目录。若自动探测到这些位置，启用时会给出明确错误；仍可使用 `profile update` 手动更新。

## 脚本与命令行

CLI 支持中英文表格输出和稳定的 JSON/JSON Lines 输出，也支持 `NO_COLOR`。语言只影响面向人的文字，不改变 JSON 字段名和数据类型。

状态、配置、节点、连接等非流式业务命令可使用 `--output json`；日志的 JSON 模式按行输出独立 JSON 对象：

```bash
mihomoctl status --output json
mihomoctl logs --follow --output json
```

脚本可依赖退出码：`0` 成功、`1` 操作失败、`2` 输入无效、`3` 权限失败、`4` Mihomo 或控制器不可用。

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

## 权限与敏感信息

- 状态、节点、测速、连接和日志通过本地 Mihomo API 完成，正常情况下不需要提权。
- 初始化、配置/订阅变更、TUN、systemd 服务和 timer 操作会按需调用 `sudo`；TUI 会显示系统原生密码提示，提权命令不会经过 shell。
- 多个 CLI、TUI 或 timer 同时修改配置时会自动排队；等待期间按 `Ctrl+C` 可以取消尚未开始的操作。
- 管理员操作只会重启安装在 root 管理目录、普通用户不可写的 mihomoctl；直接运行源码目录中的构建产物时，请先完成系统安装。
- 普通用户自动提权只使用默认的 root 状态、数据、systemd unit 和 Mihomo 探测结果；自定义系统路径与 `init --service/--config` 只允许独立 root 会话使用。用户侧 `MIHOMOCTL_CLIENT_CONFIG`/`XDG_CONFIG_HOME` 仍可使用，但必须位于调用者主目录内，且既有父目录必须属于调用者。
- 以 root 读取或应用配置时，Mihomo 二进制、活动配置以及配置、数据和备份目录必须由 root 管理，路径中不能包含符号链接或普通用户可写的目录；`/tmp` 这类 root 所有的 sticky 目录只能作为上级目录。活动配置必须是普通文件且不超过 10 MiB，FIFO 等特殊文件会被拒绝。
- `sudo` 凭据仍在系统缓存期内，或当前用户已配置 `NOPASSWD` 时，操作会直接继续。缓存超时由系统 `sudoers` 决定；mihomoctl 不会主动刷新或延长缓存，超时后的下一次提权操作会重新提示。
- 认证失败会终止当前操作并返回 TUI；在密码提示完成前按 `Ctrl+C` 会安全退出程序。这两种情况都不会启动提权后的配置或服务操作。
- 若密码提示不可用或持续认证失败，可先退出 TUI 执行 `sudo -v` 验证并缓存凭据，再以普通用户重新运行 `mihomoctl`；没有相应 sudo 权限时仍需由系统管理员授权。
- `/etc/mihomoctl` 权限为 `0700`。`/var/lib/mihomoctl` 为可穿越的 `0755`，其中只在根目录放置脱敏的 `0644 public.json`；`store/`、`backups/` 和敏感 YAML 仍限制为 `0700`/`0600`。
- `~/.config/mihomoctl/client.yaml` 权限为 `0600`，其中含本地控制器密钥。不要共享该文件，也不要加入版本控制。
- mihomoctl 自身产生的订阅错误与配置列表会隐藏 URL token、节点密码、UUID 和控制器密钥。
- TUI 添加配置时会遮蔽输入的订阅 URL、节点 URI 或本地路径；筛选输入仍正常显示。
- `logs` 内容来自 Mihomo 内核，可能包含目标地址或进程等运行信息；对外分享前仍应检查并脱敏。
- 带 token 的订阅 URL 直接写在命令行中可能进入 Shell 历史。可使用 `mihomoctl profile add - --name 私有订阅`，从标准输入粘贴内容后按 `Ctrl+D`。
- 下载超时默认为 30 秒，单份订阅上限为 10 MiB；无效更新不会替换最后可用配置。
- 提权读取或应用时，现有活动配置必须由 root 持有且不能由 group/other 写入；非 root Mihomo 服务应通过属组或 other 读取位访问。替换会保留原属组及 group/other 读取位，但不会复制自定义 POSIX ACL 或 xattr。
- `allow-lan` 只控制 Mihomo 代理端口是否对局域网开放；控制器仍被强制绑定到回环地址。

## 状态路径与备份恢复

配置应用前执行 `mihomo -t` 校验，通过后原子替换配置；运行中的服务会重启并接受健康检查，失败时回滚。配置备份默认保留最近 20 份。

首次初始化保存的“系统原配置”是当时配置的不可变快照，不会随订阅更新改变。可在 TUI 的“配置”页选中并激活，或运行：

```bash
mihomoctl profile use "系统原配置"
```

它与其他配置一样，激活时仍会叠加 mihomoctl 当前管理的监听、TUN 等设置。

主要状态路径：

| 路径 | 内容 |
| --- | --- |
| `/etc/mihomoctl/config.yaml` | root-only 管理状态与控制器密钥 |
| `/var/lib/mihomoctl/public.json` | 不含来源、验证器和密钥的配置元数据及当前监听网络设置，供普通用户读取 |
| `/var/lib/mihomoctl/store` | 配置和订阅快照 |
| `/var/lib/mihomoctl/backups` | 应用前的 Mihomo 配置备份 |
| `~/.config/mihomoctl/client.yaml` | 当前用户访问本地控制器所需的信息 |

公开快照不会写入订阅地址、节点凭据或控制器密钥。

## 升级与卸载

### 覆盖升级

使用相同安装方式和路径安装新版即可覆盖升级，无需先卸载。deb/rpm 使用[安装](#安装)中的对应命令，tar.gz 或源码安装重新执行原安装步骤。更换安装路径时需检查 `command -v mihomoctl`，避免仍启动另一个目录中的旧版本。

升级保留配置、订阅和备份；升级后运行 `mihomoctl version` 确认版本。软件包和源码安装会尽力从当前活动配置同步 `public.json` 中的设置快照，因此 Mihomo 停止时 TUI 仍可显示最近一次成功应用的值。旧版快照在同步完成前会显示“未知”；后续成功修改、切换或更新配置时也会自动补齐。

### 安全卸载

卸载前先停用自动更新；软件包的卸载脚本也会在真正删除软件包时执行此操作：

```bash
mihomoctl schedule disable
sudo apt remove mihomoctl       # Debian/Ubuntu
# 或：sudo dnf remove mihomoctl # Fedora/RHEL
```

本指南的 tar.gz 安装和默认源码安装都位于 `/usr`，可这样移除程序、文档和 timer：

```bash
sudo systemctl disable --now mihomoctl-update.timer
sudo systemctl stop mihomoctl-update.service
sudo rm -f /usr/bin/mihomoctl
sudo rm -f /usr/lib/systemd/system/mihomoctl-update.service
sudo rm -f /usr/lib/systemd/system/mihomoctl-update.timer
sudo rm -f /usr/share/doc/mihomoctl/LICENSE
sudo rm -f /usr/share/doc/mihomoctl/README.md
sudo rm -f /usr/share/doc/mihomoctl/GUIDE.md
sudo rmdir /usr/share/doc/mihomoctl 2>/dev/null || true
sudo systemctl daemon-reload
```

使用 `--prefix /usr/local` 安装时，将上述 `/usr` 替换为 `/usr/local`。卸载默认保留 `/etc/mihomoctl`、`/var/lib/mihomoctl`、用户 client 文件和 Mihomo 配置；确认 Mihomo 已能独立运行且备份不再需要后，再手动清理这些数据。删除数据不可恢复。

## 源码构建与开发

在项目源码目录中，使用 Go 1.27 构建：

```bash
make test
make build
./dist/mihomoctl version
```

安装当前源码：

```bash
./scripts/install.sh
```

脚本先以当前用户构建，再只对安装步骤使用 `sudo`。可使用 `--prefix /usr/local`，或使用 `--no-units` 跳过 systemd unit 安装。程序、文档和所需状态目录仍会安装；`DESTDIR` 可供打包和无副作用的安装测试使用。

开发检查与本地发行快照：

```bash
make check
make test-race
make snapshot
```

`make snapshot` 需要 GoReleaser v2.10 或更新版本及 Syft，产物写入 `dist/`。

## 许可证

[MIT](LICENSE)
