package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// This file gives the host a settings file. It exists because "one exe you
// double-click" removes the thing that used to supply configuration: a
// PowerShell launcher that set PORT / GROK_BIN / FE_TOKEN / HUB_URL as
// environment variables before exec'ing the binary. Nothing sources that when
// the exe is started from Explorer, so without a file the host would come up in
// local mode with no token — the settings would not be reported missing, they
// would silently not exist.
//
// Environment variables still win, so shell and service launches are unchanged.

// ConfigFileName is the settings file inside AppDir.
const ConfigFileName = "config.toml"

// AppDir is where the host keeps its settings, logs and hub token. It matches
// the directory the hub client already defaults its state file to, so all
// per-user state lives in one place.
func AppDir() string {
	if d := strings.TrimSpace(os.Getenv("CAPRI_HOST_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".capri-host")
}

// ConfigPath is the full path to the settings file.
func ConfigPath() string { return filepath.Join(AppDir(), ConfigFileName) }

// LogDir is where the rotating log file lives.
func LogDir() string { return filepath.Join(AppDir(), "logs") }

// fileConfig mirrors config.toml. Every field is a pointer so an absent key is
// distinguishable from one explicitly set to a zero value.
type fileConfig struct {
	Port        *int    `toml:"port"`
	GrokBin     *string `toml:"grok_bin"`
	HostID      *string `toml:"host_id"`
	HostName    *string `toml:"host_name"`
	FEToken     *string `toml:"fe_token"`
	HubURL      *string `toml:"hub_url"`
	HubPairCode *string `toml:"hub_pair_code"`
	HostToken   *string `toml:"host_token"`
	HubQUICPin  *string `toml:"hub_quic_pin"`
}

// loadFile reads config.toml if present. A missing file is not an error; a
// malformed one is, because silently ignoring a typo'd settings file is how a
// host ends up unpaired with no explanation.
func loadFile(path string) (*fileConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	var fc fileConfig
	if err := toml.Unmarshal(b, &fc); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return &fc, nil
}

// apply overlays file settings onto c. Called before the environment is read,
// so env keeps priority.
func (fc *fileConfig) apply(c *Config) {
	if fc == nil {
		return
	}
	setInt(&c.Port, fc.Port)
	setStr(&c.GrokBin, fc.GrokBin)
	setStr(&c.HostID, fc.HostID)
	setStr(&c.HostName, fc.HostName)
	setStr(&c.AccessToken, fc.FEToken)
	setStr(&c.HubURL, fc.HubURL)
	setStr(&c.HubPairCode, fc.HubPairCode)
	setStr(&c.HostToken, fc.HostToken)
	setStr(&c.HubQUICPin, fc.HubQUICPin)
}

func setInt(dst *int, src *int) {
	if src != nil && *src > 0 {
		*dst = *src
	}
}

func setStr(dst *string, src *string) {
	if src != nil {
		if v := strings.TrimSpace(*src); v != "" {
			*dst = v
		}
	}
}

func setBool(dst *bool, src *bool) {
	if src != nil {
		*dst = *src
	}
}

// psEnvLine matches the `$env:NAME = "value"` assignments the old launcher
// used. Commented-out lines are skipped by the leading anchor.
var psEnvLine = regexp.MustCompile(`(?mi)^\s*\$env:([A-Z_][A-Z0-9_]*)\s*=\s*"([^"]*)"`)

// MigrateLegacyEnv converts a leftover env.ps1 into config.toml when no
// config.toml exists yet, and reports the path written.
//
// Without this, upgrading to the single exe would look like a regression: the
// old launcher held the only copy of HUB_URL and FE_TOKEN, so double-clicking
// the new binary would come up in local mode and the user would reasonably
// conclude the new build was broken. Values are copied verbatim; nothing is
// deleted, so the old launcher keeps working if they want to go back.
func MigrateLegacyEnv() (written string, err error) {
	cfgPath := ConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		return "", nil // already configured; never overwrite
	}
	legacy := filepath.Join(AppDir(), "env.ps1")
	b, err := os.ReadFile(legacy)
	if err != nil {
		return "", nil // nothing to migrate
	}

	vals := map[string]string{}
	for _, m := range psEnvLine.FindAllStringSubmatch(string(b), -1) {
		vals[strings.ToUpper(m[1])] = m[2]
	}
	if len(vals) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("# capri-host 设置。改完后重启 capri-host 即可。\n")
	sb.WriteString("# 由旧的 env.ps1 自动迁移生成；环境变量仍然优先于本文件。\n\n")
	writeKV := func(key, envName string, quote bool) {
		v, ok := vals[envName]
		if !ok || strings.TrimSpace(v) == "" {
			return
		}
		if quote {
			// TOML basic strings treat \ as an escape, which matters for
			// Windows paths like C:\Users\… — a literal string does not.
			sb.WriteString(fmt.Sprintf("%s = '%s'\n", key, strings.ReplaceAll(v, "'", "")))
			return
		}
		sb.WriteString(fmt.Sprintf("%s = %s\n", key, v))
	}
	writeKV("port", "PORT", false)
	writeKV("grok_bin", "GROK_BIN", true)
	writeKV("host_id", "HOST_ID", true)
	writeKV("host_name", "HOST_NAME", true)
	writeKV("fe_token", "FE_TOKEN", true)
	writeKV("hub_url", "HUB_URL", true)
	writeKV("hub_pair_code", "HUB_PAIR_CODE", true)
	writeKV("host_token", "HOST_TOKEN", true)
	writeKV("hub_quic_pin", "HUB_QUIC_PIN", true)

	if err := os.MkdirAll(AppDir(), 0o755); err != nil {
		return "", err
	}
	// 0600: this file carries FE_TOKEN and possibly HOST_TOKEN, the same
	// secrets the hub state file is written with.
	if err := os.WriteFile(cfgPath, []byte(sb.String()), 0o600); err != nil {
		return "", err
	}
	return cfgPath, nil
}

// EnsureGrokOnPath prepends the grok binary's directory to PATH when GrokBin is
// an absolute path, mirroring what the old launcher did. Some grok builds
// resolve siblings (helpers, DLLs) relative to PATH rather than to their own
// image, so skipping this makes the agent fail to start with a confusing
// "not found" for something that is plainly on disk.
func EnsureGrokOnPath(grokBin string) {
	if grokBin == "" || !filepath.IsAbs(grokBin) {
		return
	}
	dir := filepath.Dir(grokBin)
	if dir == "" || dir == "." {
		return
	}
	cur := os.Getenv("PATH")
	for _, p := range filepath.SplitList(cur) {
		if strings.EqualFold(strings.TrimRight(p, string(os.PathSeparator)),
			strings.TrimRight(dir, string(os.PathSeparator))) {
			return
		}
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+cur)
}

// ---- config.json (macOS Capri.app / upstream) ----

const (
	// DirName 是用户主目录下的配置目录名。
	DirName = ".capri-host"
	// FileName 是 GUI / 手写配置的文件名。
	FileName = "config.json"
	// HomeEnv 覆盖配置目录（测试与便携安装）。未设则用 ~/.capri-host。
	HomeEnv = "CAPRI_HOME"
)

// File 是 ~/.capri-host/config.json 的磁盘形状。环境变量仍然覆盖这里的字段。
// 带 json 标签的 app 专用项 host 的 Load() 会读到但忽略。
type File struct {
	Bind        string `json:"bind,omitempty"`
	Port        int    `json:"port,omitempty"`
	HostID      string `json:"host_id,omitempty"`
	HostName    string `json:"host_name,omitempty"`
	HubURL      string `json:"hub_url,omitempty"`
	HubPairCode string `json:"hub_pair_code,omitempty"`
	FEToken     string `json:"fe_token,omitempty"`
	GrokBin     string `json:"grok_bin,omitempty"`
	HubQUICPin  string `json:"hub_quic_pin,omitempty"`

	// Proxy / NoProxy 是出网代理：host 启动时导出成 HTTPS_PROXY 等标准
	// 变量，hub 中继与本机拉起的 grok agent 都跟着走。留空时 macOS 上
	// 改读系统网络设置里的 HTTP/HTTPS/SOCKS 代理；系统也没开才是直连。
	Proxy   string `json:"proxy,omitempty"`
	NoProxy string `json:"no_proxy,omitempty"`

	// StartHostOnLaunch 由菜单栏应用读取：打开应用时是否自动拉起 host。
	// nil = 缺省（视为 true）。
	StartHostOnLaunch *bool `json:"start_host_on_launch,omitempty"`
	// StartAtLogin 由菜单栏应用读取并同步到系统登录项。
	StartAtLogin *bool `json:"start_at_login,omitempty"`
	// KeepAwake 由托盘读取：是否阻止系统休眠。
	KeepAwake *bool `json:"keep_awake,omitempty"`
}

// ShouldKeepAwake 缺省为 false。
func (f File) ShouldKeepAwake() bool {
	if f.KeepAwake == nil {
		return false
	}
	return *f.KeepAwake
}

// ShouldStartHostOnLaunch 缺省为 true（第一次装上就想用）。
func (f File) ShouldStartHostOnLaunch() bool {
	if f.StartHostOnLaunch == nil {
		return true
	}
	return *f.StartHostOnLaunch
}

// ShouldStartAtLogin 缺省为 false，要用户显式勾选。
func (f File) ShouldStartAtLogin() bool {
	if f.StartAtLogin == nil {
		return false
	}
	return *f.StartAtLogin
}

// ListenPort 是文件里的端口，0 / 缺省视为 8765。
func (f File) ListenPort() int {
	if f.Port > 0 {
		return f.Port
	}
	return 8765
}

// Dir 是配置目录：CAPRI_HOME 或 ~/.capri-host。
func Dir() string {
	if v := strings.TrimSpace(os.Getenv(HomeEnv)); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return DirName
	}
	return filepath.Join(home, DirName)
}

// Path 是 config.json 的绝对路径。
func Path() string {
	return filepath.Join(Dir(), FileName)
}

// HubStatePath 是配对凭证 hub.json 的绝对路径。
//
// 三个程序都要碰这个文件：host 启动时读它来免配对码重连，Capri.app 与
// Capri.exe 读它来分辨「已经配对过」和「需要新配对码」。路径在这里推导
// 一次，免得三处各写一份再慢慢漂开。
func HubStatePath() string {
	return filepath.Join(Dir(), "hub.json")
}

// LoadFile 读 config.json。文件不存在返回零值 File、nil error。
func LoadFile() (File, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return File{}, nil
		}
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, err
	}
	return f, nil
}

// UpdateFile applies fn to the config.json on disk, creating it when absent.
//
// Read-modify-write on purpose: this file is shared. Capri.app owns the
// start_host_on_launch / start_at_login keys, the user may have hand-edited
// anything, and the host only ever wants to stamp one key of its own — writing
// a File built from scratch would silently drop the rest.
//
// The lock is cross-process: the host, the tray and Capri.app all write this
// file, and an in-process mutex would not stop the other two.
func UpdateFile(fn func(*File)) error {
	return withFileLock(func() error {
		f, err := LoadFile()
		if err != nil {
			return err
		}
		fn(&f)
		return saveFile(f)
	})
}

// SaveFile 原子写入 config.json（目录 0755，文件 0600：里面可能有 FE_TOKEN）。
func SaveFile(f File) error {
	return withFileLock(func() error { return saveFile(f) })
}

func withFileLock(fn func() error) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(Dir(), FileName+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flock(f); err != nil {
		return err
	}
	defer funlock(f)
	return fn()
}

func saveFile(f File) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, FileName+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, Path()); err != nil {
		return err
	}
	ok = true
	return nil
}
