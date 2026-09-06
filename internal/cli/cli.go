package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/tui"
)

const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitInvalid     = 2
	ExitPermission  = 3
	ExitUnavailable = 4
	maxProfileInput = 10 << 20
)

var logSampleTimeout = 2 * time.Second

var isTerminalStream = func(stream any) bool {
	file, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(file.Fd())
}

// Backend contains application operations without any CLI presentation concerns.
type Backend interface {
	tui.Backend

	Initialize(context.Context, domain.InitOptions) error
	UpdateProfiles(context.Context, domain.ProfileUpdateOptions) error
	SetConfig(context.Context, string, string) error
	SyncPublicState(context.Context) error
	ScheduleStatus(context.Context) (domain.ScheduleStatus, error)
	Doctor(context.Context, bool) ([]domain.DoctorCheck, error)
}

// TUIRunner is implemented by a backend that can launch the interactive UI.
type TUIRunner interface {
	RunTUI() error
}

type contextTUIRunner interface {
	RunTUIContext(context.Context) error
}

// ExitError lets validation and backend layers select one of mihomoctl's stable
// process exit codes.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }
func (e *ExitError) ExitCode() int { return e.Code }

type exitCoder interface {
	ExitCode() int
}

type application struct {
	backend Backend
	stdout  io.Writer
	stderr  io.Writer
	output  string
	noColor bool
}

// New constructs the full mihomoctl command tree.
func New(backend Backend, stdout, stderr io.Writer) *cobra.Command {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	a := &application{backend: backend, stdout: stdout, stderr: stderr, output: "table"}

	root := &cobra.Command{
		Use:           "mihomoctl",
		Short:         "简洁的 Mihomo 命令行控制器",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          rootArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isTerminalStream(cmd.InOrStdin()) || !isTerminalStream(a.stdout) {
				return cmd.Help()
			}
			if runner, ok := backend.(contextTUIRunner); ok {
				run := func() error { return runner.RunTUIContext(cmd.Context()) }
				if a.noColor {
					return runWithNoColor(run)
				}
				return run()
			}
			if runner, ok := backend.(TUIRunner); ok {
				if a.noColor {
					return runWithNoColor(runner.RunTUI)
				}
				return runner.RunTUI()
			}
			return cmd.Help()
		},
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			switch a.output {
			case "table", "json":
				return nil
			default:
				return invalidf("--output 必须是 table 或 json")
			}
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &ExitError{Code: ExitInvalid, Err: err}
	})
	root.PersistentFlags().StringVarP(&a.output, "output", "o", "table", "输出格式：table 或 json")
	root.PersistentFlags().BoolVar(&a.noColor, "no-color", false, "禁用彩色输出")

	root.AddCommand(
		a.initCommand(),
		a.statusCommand(),
		a.serviceCommand(),
		a.modeCommand(),
		a.tunCommand(),
		a.configCommand(),
		a.proxyCommand(),
		a.profileCommand(),
		a.connectionsCommand(),
		a.logsCommand(),
		a.scheduleCommand(),
		a.doctorCommand(),
		a.completionCommand(root),
		a.versionCommand(),
	)
	return root
}

// Execute runs mihomoctl, writes failures to stderr, and returns a stable exit
// code suitable for os.Exit.
func Execute(ctx context.Context, backend Backend, stdout, stderr io.Writer) int {
	cmd := New(backend, stdout, stderr)
	if err := cmd.ExecuteContext(ctx); err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "mihomoctl: %s\n", cleanCell(err.Error()))
		}
		return exitCode(err)
	}
	return ExitOK
}

func exitCode(err error) int {
	var coded exitCoder
	if errors.As(err, &coded) {
		code := coded.ExitCode()
		if code >= ExitFailure && code <= ExitUnavailable {
			return code
		}
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, os.ErrPermission) {
		return ExitPermission
	}
	if strings.HasPrefix(err.Error(), "unknown command ") {
		return ExitInvalid
	}
	return ExitFailure
}

func invalidf(format string, args ...any) error {
	return &ExitError{Code: ExitInvalid, Err: fmt.Errorf(format, args...)}
}

func (a *application) initCommand() *cobra.Command {
	var options domain.InitOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "接管现有 Mihomo 配置",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateControllerAddress(options.Controller); err != nil {
				return err
			}
			if err := a.backend.Initialize(cmd.Context(), options); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"initialized": true}, "初始化完成")
		},
	}
	cmd.Flags().StringVar(&options.Service, "service", "", "systemd 服务名")
	cmd.Flags().StringVar(&options.ConfigPath, "config", "", "Mihomo 配置路径")
	cmd.Flags().StringVar(&options.Controller, "controller", "", "本地控制器地址")
	cmd.Flags().BoolVar(&options.Force, "force", false, "重新初始化已有配置")
	return cmd
}

func validateControllerAddress(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.Contains(value, "://") {
		return invalidf("--controller 应使用 host:port 格式")
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return invalidf("--controller 地址无效: %v", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return invalidf("--controller 只能监听回环地址")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return invalidf("--controller 端口必须是 1 到 65535")
	}
	return nil
}

func (a *application) statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "显示运行状态",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := a.backend.Status(cmd.Context())
			if err != nil {
				return err
			}
			return a.writeStatus(status)
		},
	}
}

func (a *application) serviceCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "管理 Mihomo 服务", Args: noArgs}
	for _, action := range []string{"start", "stop", "restart", "disable"} {
		action := action
		cmd.AddCommand(&cobra.Command{
			Use:   action,
			Short: serviceDescription(action),
			Args:  noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := a.backend.Service(cmd.Context(), action); err != nil {
					return err
				}
				return a.writeResult(map[string]any{"action": action}, serviceDescription(action)+"完成")
			},
		})
	}

	var now bool
	enable := &cobra.Command{
		Use:   "enable",
		Short: "启用开机启动",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.backend.Service(cmd.Context(), "enable"); err != nil {
				return err
			}
			if now {
				if err := a.backend.Service(cmd.Context(), "start"); err != nil {
					return err
				}
			}
			return a.writeResult(map[string]any{"action": "enable", "started": now}, "开机启动已启用")
		},
	}
	enable.Flags().BoolVar(&now, "now", false, "启用后立即启动服务")
	cmd.AddCommand(enable)
	return cmd
}

func serviceDescription(action string) string {
	return map[string]string{
		"start": "启动服务", "stop": "停止服务", "restart": "重启服务", "disable": "禁用开机启动",
	}[action]
}

func (a *application) modeCommand() *cobra.Command {
	return &cobra.Command{
		Use:       "mode <rule|global|direct>",
		Short:     "切换运行模式",
		Args:      exactArgs(1),
		ValidArgs: []string{string(domain.ModeRule), string(domain.ModeGlobal), string(domain.ModeDirect)},
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := domain.Mode(strings.ToLower(args[0]))
			switch mode {
			case domain.ModeRule, domain.ModeGlobal, domain.ModeDirect:
			default:
				return invalidf("未知模式 %q；可用值为 rule、global、direct", args[0])
			}
			if err := a.backend.SetMode(cmd.Context(), mode); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"mode": mode}, "模式已切换为 "+string(mode))
		},
	}
}

func (a *application) tunCommand() *cobra.Command {
	return &cobra.Command{
		Use:       "tun <on|off>",
		Short:     "启用或关闭 TUN",
		Args:      exactArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "on" && args[0] != "off" {
				return invalidf("TUN 状态必须是 on 或 off")
			}
			enabled := args[0] == "on"
			if err := a.backend.SetTUN(cmd.Context(), enabled); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"tun": enabled}, "TUN 已"+map[bool]string{true: "启用", false: "关闭"}[enabled])
		},
	}
}

func (a *application) configCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "查看或修改受管配置", Args: noArgs}
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "显示受管配置",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := a.backend.Status(cmd.Context())
			if err != nil {
				return err
			}
			return a.writeConfig(status)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "修改一项受管配置",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, err := validateConfig(args[0], args[1])
			if err != nil {
				return err
			}
			if err := a.backend.SetConfig(cmd.Context(), key, value); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"key": key, "value": value}, key+" 已更新")
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:    "sync-public-state",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.backend.SyncPublicState(cmd.Context()); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"synced": true}, "公开状态已同步")
		},
	})
	return cmd
}

func validateConfig(key, value string) (string, string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	switch key {
	case "mixed-port":
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return "", "", invalidf("mixed-port 必须是 1 到 65535 之间的整数")
		}
		return key, strconv.Itoa(port), nil
	case "allow-lan", "ipv6":
		enabled, err := parseOnOff(value)
		if err != nil {
			return "", "", invalidf("%s 必须是 true/false 或 on/off", key)
		}
		return key, strconv.FormatBool(enabled), nil
	case "log-level":
		value = strings.ToLower(value)
		switch value {
		case "silent", "error", "warning", "info", "debug":
			return key, value, nil
		default:
			return "", "", invalidf("log-level 必须是 silent、error、warning、info 或 debug")
		}
	default:
		return "", "", invalidf("不支持配置项 %q；可修改 mixed-port、allow-lan、ipv6、log-level", key)
	}
}

func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "true", "on", "1", "yes":
		return true, nil
	case "false", "off", "0", "no":
		return false, nil
	default:
		return false, errors.New("not a boolean")
	}
}

func (a *application) proxyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "proxy", Short: "查看、选择和测试代理", Args: noArgs}
	var groupFilter string
	list := &cobra.Command{
		Use:   "list",
		Short: "列出代理节点",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			groups, err := a.backend.Groups(cmd.Context())
			if err != nil {
				return err
			}
			if groupFilter != "" {
				groups = filterGroups(groups, groupFilter)
				if len(groups) == 0 {
					return invalidf("找不到策略组 %q", groupFilter)
				}
			}
			return a.writeGroups(groups)
		},
	}
	list.Flags().StringVar(&groupFilter, "group", "", "只显示指定策略组")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use:   "select <group> <proxy>",
		Short: "选择策略组节点",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonBlank("策略组", args[0]); err != nil {
				return err
			}
			if err := requireNonBlank("代理名称", args[1]); err != nil {
				return err
			}
			if err := a.backend.SelectProxy(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"group": args[0], "proxy": args[1]}, args[0]+" 已切换到 "+args[1])
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "test <group>",
		Short: "测试策略组的直接成员",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonBlank("策略组", args[0]); err != nil {
				return err
			}
			delays, err := a.backend.TestGroup(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.writeDelays(args[0], delays)
		},
	})
	return cmd
}

func filterGroups(groups []domain.ProxyGroup, name string) []domain.ProxyGroup {
	result := make([]domain.ProxyGroup, 0, 1)
	for _, group := range groups {
		if group.Name == name {
			result = append(result, group)
		}
	}
	return result
}

func (a *application) profileCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "管理配置与订阅", Args: noArgs}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "列出配置",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profiles, err := a.backend.Profiles(cmd.Context())
			if err != nil {
				return err
			}
			return a.writeProfiles(profiles)
		},
	})

	var name string
	var interval time.Duration
	add := &cobra.Command{
		Use:   "add <source>",
		Short: "添加远程订阅、本地配置或节点 URI；- 表示标准输入",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 {
				return invalidf("--interval 必须大于零")
			}
			source := args[0]
			if source == "-" {
				var err error
				source, err = readProfileInput(cmd.InOrStdin())
				if err != nil {
					return err
				}
			}
			if err := requireNonBlank("配置来源", source); err != nil {
				return err
			}
			if err := a.backend.AddProfile(cmd.Context(), name, source, interval); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"name": name, "source_added": true, "update_interval": interval.String()}, "配置已添加")
		},
	}
	add.Flags().StringVar(&name, "name", "", "配置名称（默认从来源推导）")
	add.Flags().DurationVar(&interval, "interval", 24*time.Hour, "远程订阅更新间隔")
	cmd.AddCommand(add)

	var all, due bool
	update := &cobra.Command{
		Use:   "update [name]",
		Short: "更新配置或订阅",
		Args:  maximumArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selectors := len(args)
			if all {
				selectors++
			}
			if due {
				selectors++
			}
			if selectors != 1 {
				return invalidf("请指定配置名称、--all 或 --due，且只能选择一种")
			}
			options := domain.ProfileUpdateOptions{All: all, Due: due}
			if len(args) == 1 {
				if err := requireNonBlank("配置名称", args[0]); err != nil {
					return err
				}
				options.Name = args[0]
			}
			if err := a.backend.UpdateProfiles(cmd.Context(), options); err != nil {
				return err
			}
			return a.writeResult(options, "配置已更新")
		},
	}
	update.Flags().BoolVar(&all, "all", false, "更新全部配置")
	update.Flags().BoolVar(&due, "due", false, "只更新已到期配置")
	cmd.AddCommand(update)

	for _, action := range []string{"use", "remove"} {
		action := action
		cmd.AddCommand(&cobra.Command{
			Use:   action + " <name>",
			Short: map[string]string{"use": "激活配置", "remove": "删除配置"}[action],
			Args:  exactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := requireNonBlank("配置名称", args[0]); err != nil {
					return err
				}
				var err error
				if action == "use" {
					err = a.backend.UseProfile(cmd.Context(), args[0])
				} else {
					err = a.backend.RemoveProfile(cmd.Context(), args[0])
				}
				if err != nil {
					return err
				}
				return a.writeResult(map[string]any{"action": action, "name": args[0]}, map[string]string{"use": "配置已激活", "remove": "配置已删除"}[action])
			},
		})
	}
	return cmd
}

func (a *application) connectionsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "connections", Short: "查看和关闭活动连接", Args: noArgs}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "列出活动连接",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			connections, err := a.backend.Connections(cmd.Context())
			if err != nil {
				return err
			}
			return a.writeConnections(connections)
		},
	})
	var all bool
	closeCommand := &cobra.Command{
		Use:   "close [id]",
		Short: "关闭一条或全部连接",
		Args:  maximumArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 0) == !all {
				return invalidf("请指定连接 ID 或 --all，且只能选择一种")
			}
			if all {
				if err := a.backend.CloseAllConnections(cmd.Context()); err != nil {
					return err
				}
				return a.writeResult(map[string]any{"closed": "all"}, "全部连接已关闭")
			}
			if err := requireNonBlank("连接 ID", args[0]); err != nil {
				return err
			}
			if err := a.backend.CloseConnection(cmd.Context(), args[0]); err != nil {
				return err
			}
			return a.writeResult(map[string]any{"closed": args[0]}, "连接已关闭")
		},
	}
	closeCommand.Flags().BoolVar(&all, "all", false, "关闭全部连接")
	cmd.AddCommand(closeCommand)
	return cmd
}

func (a *application) logsCommand() *cobra.Command {
	var follow bool
	var level string
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "读取 Mihomo 日志",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			level = strings.ToLower(level)
			switch level {
			case "debug", "info", "warning", "error", "silent":
			default:
				return invalidf("--level 必须是 debug、info、warning、error 或 silent")
			}
			logContext, cancelLogs := context.WithCancel(cmd.Context())
			defer cancelLogs()
			entries, errs := a.backend.WatchLogs(logContext, level)
			if entries == nil && errs == nil {
				return errors.New("日志流不可用")
			}
			var sampleTimer *time.Timer
			var sampleExpired <-chan time.Time
			if !follow {
				sampleTimer = time.NewTimer(logSampleTimeout)
				sampleExpired = sampleTimer.C
				defer sampleTimer.Stop()
			}
			for entries != nil || errs != nil {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-sampleExpired:
					return nil
				case entry, ok := <-entries:
					if !ok {
						entries = nil
						continue
					}
					if err := a.writeLog(entry); err != nil {
						return err
					}
					if !follow {
						return nil
					}
				case err, ok := <-errs:
					if !ok {
						errs = nil
						continue
					}
					if err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "持续输出日志")
	cmd.Flags().StringVar(&level, "level", "info", "最低日志级别")
	return cmd
}

func (a *application) scheduleCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "管理订阅自动更新", Args: noArgs}
	for _, setting := range []struct {
		name    string
		enabled bool
	}{
		{name: "enable", enabled: true},
		{name: "disable", enabled: false},
	} {
		setting := setting
		cmd.AddCommand(&cobra.Command{
			Use:   setting.name,
			Short: map[bool]string{true: "启用自动更新", false: "关闭自动更新"}[setting.enabled],
			Args:  noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := a.backend.SetSchedule(cmd.Context(), setting.enabled); err != nil {
					return err
				}
				return a.writeResult(map[string]any{"enabled": setting.enabled}, map[bool]string{true: "自动更新已启用", false: "自动更新已关闭"}[setting.enabled])
			},
		})
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "显示自动更新状态",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := a.backend.ScheduleStatus(cmd.Context())
			if err != nil {
				return err
			}
			return a.writeScheduleStatus(status)
		},
	})
	return cmd
}

func (a *application) doctorCommand() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "检查 Mihomo 和 mihomoctl 环境",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks, err := a.backend.Doctor(cmd.Context(), fix)
			if err != nil {
				return err
			}
			return a.writeDoctor(checks)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "尝试修复可自动处理的问题")
	return cmd
}

func (a *application) completionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion <bash|zsh|fish>",
		Short:     "生成 Shell 补全脚本",
		Args:      exactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(_ *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(a.stdout)
			case "zsh":
				return root.GenZshCompletion(a.stdout)
			case "fish":
				return root.GenFishCompletion(a.stdout, true)
			default:
				return invalidf("不支持 Shell %q", args[0])
			}
		},
	}
}

func (a *application) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "显示版本信息",
		Args:  noArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			info := versionInfo{Version: domain.Version, Commit: domain.Commit, Date: domain.Date}
			if a.output == "json" {
				return writeJSON(a.stdout, info)
			}
			return writeTable(a.stdout, []string{"VERSION", "COMMIT", "BUILT"}, [][]string{{info.Version, info.Commit, info.Date}})
		},
	}
}

type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readProfileInput(reader io.Reader) (string, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxProfileInput+1))
	if err != nil {
		return "", fmt.Errorf("读取标准输入失败: %w", err)
	}
	if len(content) > maxProfileInput {
		return "", invalidf("标准输入不能超过 10 MiB")
	}
	if len(content) == 0 {
		return "", invalidf("标准输入为空")
	}
	return string(content), nil
}

func runWithNoColor(run func() error) (err error) {
	if _, exists := os.LookupEnv("NO_COLOR"); exists {
		return run()
	}
	if err := os.Setenv("NO_COLOR", "1"); err != nil {
		return fmt.Errorf("设置 NO_COLOR 失败: %w", err)
	}
	defer func() { err = errors.Join(err, os.Unsetenv("NO_COLOR")) }()
	return run()
}

func requireNonBlank(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidf("%s不能为空", name)
	}
	return nil
}

func rootArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return invalidf("未知命令 %q", args[0])
	}
	return nil
}

func noArgs(cmd *cobra.Command, args []string) error {
	return exactArgs(0)(cmd, args)
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != count {
			return invalidf("%s 需要 %d 个参数，实际收到 %d 个", cmd.CommandPath(), count, len(args))
		}
		return nil
	}
}

func maximumArgs(count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > count {
			return invalidf("%s 最多接受 %d 个参数，实际收到 %d 个", cmd.CommandPath(), count, len(args))
		}
		return nil
	}
}
