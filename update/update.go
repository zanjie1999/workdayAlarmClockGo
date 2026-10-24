/*
 * 自动更新：从 GitHub Release 检查新版本并下载替换自身
 *
 * 运行中的可执行文件在 Linux 上不能直接以写方式打开（ETXTBSY），
 * 但可以先下载到同目录的临时文件，再用 rename 覆盖原文件：
 * 代码段和数据段已经映射进内存，替换目录项不会影响正在运行的进程，
 * 之后再 exec 一次自己就完成升级了。
 */

package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zanjie1999/httpme"
)

const (
	// DefaultProxy GitHub 下载加速前缀，留空则直连
	DefaultProxy = "https://ghproxy.net/"
	// 检查更新的缓存时间，避免频繁请求 api.github.com 被限流
	checkCacheTime = 5 * time.Minute
)

// Asset Release 里的一个文件
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Release GitHub Release 信息
type Release struct {
	Tag    string  `json:"tag_name"`
	Name   string  `json:"name"`
	Notes  string  `json:"body"`
	Assets []Asset `json:"assets"`
}

// Plan 一次更新计划
type Plan struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Release   string `json:"releaseName"`
	HasUpdate bool   `json:"hasUpdate"`
	// Asset 将要下载的文件名，找不到适配当前系统的文件时为空
	Asset string `json:"asset"`
	Size  int64  `json:"size"`
	// URL 实际下载地址（已加上加速前缀）
	URL string `json:"url"`
	// RawURL GitHub 原始地址
	RawURL string `json:"rawUrl"`
}

var (
	// 当前版本号
	current = ""
	// 仓库，如 zanjie1999/workdayAlarmClockGo
	repo = ""
	// 下载加速前缀，空表示直连
	proxy = DefaultProxy

	cacheMu  sync.Mutex
	cacheAt  time.Time
	cacheRel *Release

	applyMu sync.Mutex

	// 自身路径的缓存，见 selfPath
	selfPathMu    sync.Mutex
	selfPathCache string
)

// Init 初始化，由 main 在启动时调用
func Init(version, repoName string) {
	current = strings.TrimSpace(version)
	repo = strings.Trim(strings.TrimSpace(repoName), "/")
	proxy = DefaultProxy
	// 必须在任何更新动作之前把自身路径缓存下来：
	// 替换程序时会先把自己改名成 xxx.old，之后 /proc/self/exe 就跟着指向备份文件了
	if _, err := selfPath(); err != nil {
		log.Println("缓存程序路径失败", err)
	}
}

// Current 当前版本号
func Current() string { return current }

// RepoURL 仓库地址
func RepoURL() string { return "https://github.com/" + repo }

// 把 GitHub 原始下载地址加上加速前缀
func accelerate(u string) string {
	if proxy == "" || !strings.HasPrefix(u, "http") {
		return u
	}
	if !strings.Contains(u, "github.com/") {
		return u
	}
	return proxy + u
}

/* ---------------- 版本号比较 ---------------- */

var versionSplit = regexp.MustCompile(`\d+`)

// CompareVersion 比较两个版本号，a>b 返回 1，相等返回 0，a<b 返回 -1。
// 按数字段比较，所以 42.10 > 42.5（旧代码直接比较字符串，会把降级也当成更新）。
func CompareVersion(a, b string) int {
	an, asuf := parseVersion(a)
	bn, bsuf := parseVersion(b)
	n := len(an)
	if len(bn) > n {
		n = len(bn)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	// 数字段相同再比后缀，1.0 > 1.0-beta
	switch {
	case asuf == bsuf:
		return 0
	case asuf == "":
		return 1
	case bsuf == "":
		return -1
	case asuf > bsuf:
		return 1
	default:
		return -1
	}
}

func parseVersion(v string) ([]int, string) {
	v = strings.TrimSpace(v)
	v = strings.TrimLeft(v, "vV")
	if v == "" {
		return nil, ""
	}
	var nums []int
	suffix := ""
	for _, part := range strings.Split(v, ".") {
		m := versionSplit.FindString(part)
		if m == "" {
			// 没数字的段当 0，并记下后缀
			nums = append(nums, 0)
		} else {
			d, err := strconv.Atoi(m)
			if err != nil {
				d = 0
			}
			nums = append(nums, d)
		}
		if suffix == "" {
			if idx := strings.Index(part, m); idx >= 0 && idx+len(m) < len(part) {
				suffix = part[idx+len(m):]
			}
		}
	}
	return nums, strings.TrimLeft(suffix, "-+_")
}

/* ---------------- 检查更新 ---------------- */

// PlatformAsset 返回当前系统对应的 Release 文件名
func PlatformAsset() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "workdayAlarmClock-linux"
	case "linux/386":
		return "workdayAlarmClock-linux-i386"
	case "linux/arm":
		// Release 里的 -linux-arm 就是给 armv7 设备用的（GOARM=5 的指令集在 armv7 上能跑）
		return "workdayAlarmClock-linux-arm"
	case "linux/arm64":
		return "workdayAlarmClock-linux-arm64"
	case "linux/mipsle":
		return "workdayAlarmClock-linux-mipsle"
	case "windows/amd64":
		return "workdayAlarmClock.exe"
	case "windows/386":
		return "workdayAlarmClock-i386.exe"
	case "darwin/amd64":
		return "workdayAlarmClock-darwin"
	case "darwin/arm64":
		return "workdayAlarmClock-darwin-arm64"
	}
	return ""
}

// latest 获取最新 Release，5 分钟内走缓存
func latest() (*Release, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cacheRel != nil && time.Since(cacheAt) < checkCacheTime {
		return cacheRel, nil
	}
	if repo == "" {
		return nil, errors.New("未设置更新仓库")
	}
	req := httpme.Httpme()
	// 注意 SetTimeout 的单位是“秒”，传 20 表示 20 秒
	req.SetTimeout(20)
	resp, err := req.Get("https://api.github.com/repos/"+repo+"/releases/latest",
		httpme.Header{"Accept": "application/vnd.github+json"})
	if err != nil {
		return nil, err
	}
	if resp.R.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回 %s", resp.R.Status)
	}
	var rel Release
	// 万一返回的是错误提示而不是 JSON，也走这里报错
	if err := json.Unmarshal(resp.Content(), &rel); err != nil {
		return nil, errors.New("解析 GitHub 返回失败：" + err.Error())
	}
	if rel.Tag == "" {
		return nil, errors.New("GitHub 未返回版本号")
	}
	cacheRel = &rel
	cacheAt = time.Now()
	return cacheRel, nil
}

// pickAsset 选出要下载的文件
func pickAsset(rel *Release) *Asset {
	want := PlatformAsset()
	if want == "" {
		return nil
	}
	for i := range rel.Assets {
		if strings.EqualFold(rel.Assets[i].Name, want) {
			return &rel.Assets[i]
		}
	}
	return nil
}

// Check 检查更新
func Check() (*Plan, error) {
	rel, err := latest()
	if err != nil {
		return nil, err
	}
	p := &Plan{
		Current: current,
		Latest:  rel.Tag,
		Release: rel.Name,
	}
	// 只有远端版本比当前新才提示更新，降级/同版本都不提示
	p.HasUpdate = CompareVersion(rel.Tag, current) > 0
	if a := pickAsset(rel); a != nil {
		p.Asset = a.Name
		p.Size = a.Size
		p.RawURL = a.URL
		p.URL = accelerate(a.URL)
	}
	return p, nil
}

/* ---------------- 下载替换 ---------------- */

// Apply 按计划下载并替换当前可执行文件，成功后需要调用 Restart 重启
func Apply(p *Plan) error {
	if p == nil || !p.HasUpdate {
		return errors.New("当前已是最新版本")
	}
	if p.Asset == "" {
		return fmt.Errorf("Release 里没有 %s/%s 的可用文件，请手动下载", runtime.GOOS, runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		// Windows 不允许重命名正在运行的 exe，只能手动替换
		return errors.New("Windows 下无法替换正在运行的程序，请手动下载更新")
	}
	exe, err := selfPath()
	if err != nil {
		return err
	}
	return applyTo(exe, p)
}

// applyTo 下载并替换指定的文件，单独拆出来方便测试
func applyTo(exe string, p *Plan) error {
	if p == nil || !p.HasUpdate {
		return errors.New("当前已是最新版本")
	}
	if p.Asset == "" {
		return fmt.Errorf("Release 里没有 %s/%s 的可用文件，请手动下载", runtime.GOOS, runtime.GOARCH)
	}
	applyMu.Lock()
	defer applyMu.Unlock()

	// 同目录的临时文件，保证后面的 rename 是同一个文件系统内的原子操作
	tmp := exe + ".new"
	// 优先用加速地址，失败再退回 GitHub 原始地址
	candidates := []string{}
	if p.URL != "" {
		candidates = append(candidates, p.URL)
	}
	if p.RawURL != "" && p.RawURL != p.URL {
		candidates = append(candidates, p.RawURL)
	}
	var lastErr error
	for _, u := range candidates {
		log.Println("开始下载更新", p.Latest, u)
		if err := download(u, tmp, p.Size); err != nil {
			lastErr = err
			log.Println("下载失败", err)
			os.Remove(tmp)
			continue
		}
		if err := verify(tmp); err != nil {
			lastErr = err
			log.Println("文件校验失败", err)
			os.Remove(tmp)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return lastErr
	}

	if err := replaceWith(exe, tmp, p.Size); err != nil {
		return err
	}
	log.Println("更新完成，旧版本备份在", exe+".old")
	return nil
}

// replaceWith 用 tmp 替换 exe，原来的 exe 备份成 exe.old；任何一步失败都回滚，
// 保证原路径上不会留下半截文件
func replaceWith(exe, tmp string, wantSize int64) error {
	// 保持原来的权限位
	mode := os.FileMode(0755)
	if fi, err := os.Stat(exe); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return errors.New("设置执行权限失败：" + err.Error())
	}

	// 先备份再 rename 覆盖，rename 失败还能退回去
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return errors.New("备份原程序失败（目录可能没有写权限）：" + err.Error())
	}
	// rename 会保留原来的修改时间，改成现在，方便下次启动判断备份是否还新
	now := time.Now()
	os.Chtimes(old, now, now)
	if err := os.Rename(tmp, exe); err != nil {
		// 回滚
		os.Rename(old, exe)
		os.Remove(tmp)
		return errors.New("替换程序失败：" + err.Error())
	}
	// 替换完成后再确认一次，避免把空文件或半截文件留在原路径上
	if err := checkReplaced(exe, wantSize); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("%w；回滚也失败了，请手动把 %s 改名回 %s", err, old, exe)
		}
		return err
	}
	return nil
}

// checkReplaced 校验替换后的文件大小与执行权限
func checkReplaced(exe string, wantSize int64) error {
	fi, err := os.Stat(exe)
	if err != nil {
		return errors.New("替换后找不到程序：" + err.Error())
	}
	if fi.Size() == 0 {
		return errors.New("替换后的文件是空的")
	}
	if wantSize > 0 && fi.Size() != wantSize {
		return fmt.Errorf("替换后文件大小不符，期望 %d 实际 %d", wantSize, fi.Size())
	}
	if fi.Mode().Perm()&0111 == 0 {
		return errors.New("替换后的文件没有执行权限")
	}
	return nil
}

// SelfUpdate 检查并更新，返回实际执行的计划
func SelfUpdate() (*Plan, error) {
	p, err := Check()
	if err != nil {
		return nil, err
	}
	if !p.HasUpdate {
		return p, nil
	}
	return p, Apply(p)
}

func download(url, dst string, wantSize int64) error {
	req := httpme.Httpme()
	// 单位是秒，给小水管留 30 分钟
	req.SetTimeout(1800)
	// 要的是可执行的原始字节，明确不要压缩：
	// 带了 Accept-Encoding 后 net/http 不会再自动加 gzip，httpme 也不会走解压分支
	resp, err := req.GetStream(url, httpme.Header{"Accept-Encoding": "identity"})
	if err != nil {
		return err
	}
	defer resp.R.Body.Close()
	if resp.R.StatusCode != http.StatusOK {
		return fmt.Errorf("下载返回 %s", resp.R.Status)
	}

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	defer f.Close()

	var written int64
	buf := make([]byte, 64*1024)
	lastLog := 0
	for {
		n, rerr := resp.R.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			// 每 20% 打一条日志，方便在串口/日志里看进度
			if wantSize > 0 {
				if pct := int(written * 100 / wantSize); pct/20 > lastLog/20 {
					lastLog = pct
					log.Printf("已下载 %d%% (%d/%d)", pct, written, wantSize)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if wantSize > 0 && written != wantSize {
		return fmt.Errorf("文件大小不符，期望 %d 实际 %d", wantSize, written)
	}
	if written < 512*1024 {
		return fmt.Errorf("下载内容过小（%d 字节），可能下载到了错误页面", written)
	}
	return nil
}

// verify 检查下载下来的文件是不是当前系统的可执行文件
func verify(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return err
	}
	var ok bool
	switch runtime.GOOS {
	case "windows":
		ok = head[0] == 'M' && head[1] == 'Z'
	case "darwin":
		// Mach-O 64（含 arm64）以及 fat 二进制
		ok = (head[0] == 0xcf && head[1] == 0xfa && head[2] == 0xed && head[3] == 0xfe) ||
			(head[0] == 0xca && head[1] == 0xfe && head[2] == 0xba && head[3] == 0xbe)
	default:
		ok = head[0] == 0x7f && head[1] == 'E' && head[2] == 'L' && head[3] == 'F'
	}
	if !ok {
		return errors.New("下载的文件不是可执行文件")
	}
	return nil
}

// selfPath 当前可执行文件的真实路径（解开软链接）
//
// 结果只在第一次调用时解析并缓存。Linux 的 /proc/self/exe 会跟随 rename：
// 更新流程把正在运行的程序改名成 xxx.old 之后，再解析拿到的就是备份文件的路径，
// 用这个路径 exec 只会把旧版本重新跑一遍。所以 Init 时会先预热缓存，
// 后续的 Restart、CleanupOld 一律用缓存值。
func selfPath() (string, error) {
	selfPathMu.Lock()
	defer selfPathMu.Unlock()
	if selfPathCache != "" {
		return selfPathCache, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", errors.New("获取程序路径失败：" + err.Error())
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", errors.New("获取程序路径失败：" + err.Error())
	}
	selfPathCache = abs
	return abs, nil
}

// SelfExe 返回启动时缓存下来的自身路径
func SelfExe() (string, error) { return selfPath() }

// CleanupOld 清理上次更新留下的临时文件，启动时调用。
// 旧版本备份只在它已经存在超过10分钟时才删，
// 这样新版本刚替换完就起不来时，还能用 xxx.old 退回去。
func CleanupOld() {
	exe, err := selfPath()
	if err != nil {
		return
	}
	old := exe + ".old"
	if fi, err := os.Stat(old); err == nil {
		if time.Since(fi.ModTime()) > 10*time.Minute {
			os.Remove(old)
			log.Println("已清理旧版本备份", old)
		} else {
			log.Println("上一版程序保留在", old, "如需退可直接改名覆盖")
		}
	}
	os.Remove(exe + ".new")
}
