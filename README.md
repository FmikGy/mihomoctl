# mihomoctl

面向 Linux 的简洁 Mihomo 控制前端，提供中英文终端界面（TUI）和命令行（CLI）。

## 功能

- 查看实时流量图表、连接和日志。
- 添加、更新和切换订阅或本地配置。
- 切换策略组和节点，测试节点延迟。
- 管理 Mihomo 服务、运行模式、TUN 和监听网络设置。

## 安装

### 1. 先安装 Mihomo

mihomoctl 不包含 Mihomo 内核。请先在使用 systemd 的 Linux 上安装并配置 Mihomo，准备好 `mihomo.service` 服务。

- Mihomo 官方仓库：[MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo/tree/Meta)
- Mihomo 内核下载：[Releases](https://github.com/MetaCubeX/mihomo/releases/latest)

确认内核与服务可用：

```bash
mihomo -v
systemctl status mihomo.service
```

### 2. 安装 mihomoctl

从 [mihomoctl Releases](https://github.com/FmikGy/mihomoctl/releases/latest) 下载对应系统和架构的安装包：x86_64 选择 `amd64`，aarch64 选择 `arm64`。

在下载目录执行对应命令；通配符应只匹配本次要安装的一个文件：

```bash
# Debian / Ubuntu
sudo apt install ./mihomoctl_*.deb

# Fedora / RHEL
sudo dnf install ./mihomoctl_*.rpm
```

tar.gz 安装和源码构建见[详细使用指南](GUIDE.md#安装)。

## 快速开始

首次使用先初始化，再启动终端界面：

```bash
mihomoctl init
mihomoctl
```

初始化会备份并接管现有 Mihomo 配置；服务运行时会重启并检查配置，失败自动回滚。日常以普通用户运行，管理操作会按需调用 `sudo`。

`Tab` 切换页面，`?` 查看帮助，`q` 退出。

默认使用简体中文。可在 TUI 的“设置 → 语言”中切换，或执行 `mihomoctl language en` 持久切换为英文；`--lang en` 仅覆盖当前一次运行。

## 详细文档

[完整使用指南](GUIDE.md)包含安装、订阅、节点、设置、权限、备份恢复和开发说明。

- [配置订阅](GUIDE.md#配置与订阅) · [选择节点](GUIDE.md#节点选择与测速) · [网络设置](GUIDE.md#模式与网络设置)
- [全部快捷键](GUIDE.md#tui-快捷键) · [升级与卸载](GUIDE.md#升级与卸载) · [源码构建](GUIDE.md#源码构建与开发)

## 许可证

[MIT](LICENSE)
