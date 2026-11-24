/*
 * 自动更新：从 GitHub Release 检查新版本并下载替换自身
 *
 * 运行中的可执行文件在 Linux 上既不能以写方式打开（ETXTBSY），
 * 也不能靠「删掉它」来腾空间：代码段还映射在进程里，inode 引用没归零，
 * 块要等进程 exec 到别的镜像上、或者退出之后才真正还给文件系统。
 *
 * 所以空间吃紧的设备上走「内存接力」：
 *  1. 新版本先下载进 memfd（只占内存），校验通过；
 *  2. exec 到内存里的新版本，参数带上真实路径和原有参数（PID 不变），
 *     这一刻旧文件的引用归零，删掉就真能释放空间；
 *  3. 内存里那一份删掉旧文件、把自己写到 真实路径.new、rename 落位；
 *  4. 再 exec 回真实路径，内存那一份随 exec 自动消失。
 * 磁盘上任何时刻最多只有一份程序，断电窗口只有最后那一下 rename。
 *
 * 走不了内存（内存不够 / 内核限制 / 非 Linux）就退回旁路方案：
 * 新版本下载到程序旁边的 .new，校验后 rename 覆盖，峰值需要两份空间。
 * 两条路线都在动手之前按 Release 里的大小算一次空间，不够就不更新。
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
	DefaultProxy = "https://gh-proxy.com/"
	// 检查更新的缓存时间，避免频繁请求 api.github.com 被限流
	checkCacheTime = 5 * time.Minute
	// 空间检查留的余量：新版本大小 / spaceSlack，给 fsync 和文件系统元数据
	spaceSlack = 20
	// relaySuffix 接力副本的文件名后缀，放在临时目录里，用完即删
	relaySuffix = ".run"
)

// errNoRelay 当前环境没有可用的接力方式（内核没 memfd、临时目录用不了、非 Linux），
// 调用方退回旁路方案
var errNoRelay = errors.New("没有可用的接力方式")

// tmpCandidates 接力副本可以放的临时目录，优先 /tmp（多数设备上是 tmpfs，
// 不占程序所在分区），没有再用 $TMPDIR
func tmpCandidates() []string {
	dirs := []string{"/tmp"}
	if d := strings.TrimSpace(os.Getenv("TMPDIR")); d != "" && filepath.Clean(d) != "/tmp" {
		dirs = append(dirs, d)
	}
	return dirs
}

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
	// 更新过程中当前进程会被换成别的镜像，之后 /proc/self/exe
	// 指向的可能是一个已经删除的旧 inode，路径不能再信
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

// Apply 按计划下载并替换当前可执行文件。
// 成功时进程会被新版本接管（exec 过去），不会返回；
// 返回 error 表示没换成，磁盘上的旧版本原封不动。
func Apply(p *Plan) error {
	if p == nil || !p.HasUpdate {
		return errors.New("当前已是最新版本")
	}
	if p.Asset == "" {
		return fmt.Errorf("Release 里没有 %s/%s 的可用文件，请手动下载", runtime.GOOS, runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		// Windows 不允许删除或替换正在运行的 exe，只能手动替换
		return errors.New("Windows 下无法替换正在运行的程序，请手动下载更新")
	}
	exe, err := selfPath()
	if err != nil {
		return err
	}
	return applyTo(exe, p)
}

// target 一次替换要用到的旧文件信息
type target struct {
	exe    string
	mode   os.FileMode
	size   int64  // 旧文件大小，删掉后能腾出这么多空间
	need   uint64 // 新版本要占的空间（含余量），Release 没给大小时为 0
	free   uint64 // 当前可用空间
	freeOK bool   // 查不到可用空间时为 false，跳过空间检查
}

// newTarget 收集旧文件信息和所在分区的可用空间
func newTarget(exe string) target {
	t := target{exe: exe, mode: 0755}
	if fi, err := os.Stat(exe); err == nil {
		t.mode = fi.Mode().Perm()
		t.size = fi.Size()
	}
	free, err := freeSpace(filepath.Dir(exe))
	t.free, t.freeOK = free, err == nil
	if err != nil {
		log.Println("查询", filepath.Dir(exe), "的可用空间失败，跳过空间检查")
	}
	return t
}

// notEnough 判断 avail 字节够不够放新版本，返回空串表示够
func (t target) notEnough(avail uint64) string {
	if !t.freeOK || t.need == 0 {
		return ""
	}
	if avail >= t.need {
		return ""
	}
	return fmt.Sprintf("空间不足，本次不更新：可用 %s，新版本需要 %s", humanSize(avail), humanSize(t.need))
}

// applyTo 选一条路线把新版本换上去。
//
// 优先级：接力路线（内存文件 → 临时目录）→ 旁路路线。
// 接力的好处是旧文件先被删掉再写新的，磁盘上任何时刻只占一份；
// 旁路要旧文件还在的时候再写一份新的，峰值是两倍。
func applyTo(exe string, p *Plan) error {
	applyMu.Lock()
	defer applyMu.Unlock()

	t := newTarget(exe)
	t.need = withSlack(p.Size)

	// 成功时不会返回
	err := applyViaRelay(t, p)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errNoRelay) {
		return err
	}
	log.Println("改用旁路方案：", err)

	if msg := t.notEnough(t.free); msg != "" {
		return errors.New(msg)
	}
	return applyBeside(t, p)
}

// applyBeside 把新版本下载到程序旁边的 .new，校验后 rename 覆盖并重启。
// 峰值要「旧 + 新」两份空间，但不需要额外内存。
func applyBeside(t target, p *Plan) error {
	f, err := createNew(t)
	if err != nil {
		return err
	}
	size, err := downloadPlan(p, f)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	return finishBeside(t, f, size)
}

// createNew 建好程序旁边的 .new 临时文件。
// 用读写方式打开：下载完要 ReadAt 校验文件头，只写的 fd 读不了。
func createNew(t target) (*os.File, error) {
	tmp := t.exe + ".new"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0755)
	if err != nil {
		return nil, errors.New("创建临时文件失败（目录可能没有写权限）：" + err.Error())
	}
	return f, nil
}

// finishBeside 把已经写好的 .new 收尾：刷盘、权限、改名、重启
func finishBeside(t target, f *os.File, size int64) error {
	tmp := f.Name()
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, t.mode); err != nil {
		os.Remove(tmp)
		return errors.New("设置执行权限失败：" + err.Error())
	}
	if err := os.Rename(tmp, t.exe); err != nil {
		os.Remove(tmp)
		return errors.New("替换程序失败：" + err.Error())
	}
	if err := checkReplaced(t.exe, size); err != nil {
		return err
	}
	log.Println("新版本已落位，正在重启：", t.exe)
	return Restart()
}

// downloadPlan 依次尝试加速地址和 GitHub 原始地址，写进 f 并校验文件头，返回实际字节数
func downloadPlan(p *Plan, f *os.File) (int64, error) {
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
		// 换下一个地址之前要清掉上一次留下的半截内容
		if err := f.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		n, err := downloadTo(u, f, p.Size)
		if err != nil {
			lastErr = err
			log.Println("下载失败", err)
			continue
		}
		if err := checkELF(f); err != nil {
			lastErr = err
			log.Println("文件校验失败", err)
			continue
		}
		return n, nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的下载地址")
	}
	return 0, lastErr
}

// downloadTo 把 url 的内容流式写进 w，wantSize>0 时校验长度并打印进度
func downloadTo(url string, w io.Writer, wantSize int64) (int64, error) {
	req := httpme.Httpme()
	// 单位是秒，给小水管留 30 分钟
	req.SetTimeout(1800)
	// 要的是可执行的原始字节，明确不要压缩：
	// 带了 Accept-Encoding 后 net/http 不会再自动加 gzip，httpme 也不会走解压分支
	resp, err := req.GetStream(url, httpme.Header{"Accept-Encoding": "identity"})
	if err != nil {
		return 0, err
	}
	defer resp.R.Body.Close()
	if resp.R.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("下载返回 %s", resp.R.Status)
	}

	var written int64
	buf := make([]byte, 64*1024)
	lastLog := 0
	for {
		n, rerr := resp.R.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return written, werr
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
			return written, rerr
		}
	}
	if wantSize > 0 && written != wantSize {
		return written, fmt.Errorf("文件大小不符，期望 %d 实际 %d", wantSize, written)
	}
	if written < 512*1024 {
		return written, fmt.Errorf("下载内容过小（%d 字节），可能下载到了错误页面", written)
	}
	return written, nil
}

// withSlack 给新版本大小留一点余量，0 表示大小未知
func withSlack(size int64) uint64 {
	if size <= 0 {
		return 0
	}
	return uint64(size) + uint64(size/spaceSlack)
}

// humanSize 把字节数写成人看的大小
func humanSize(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
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

// selfPath 当前可执行文件的真实路径（解开软链接）
//
// 结果只在第一次调用时解析并缓存。更新过程中进程会 exec 到别处，
// 之后 os.Executable() 拿到的是新位置（甚至是 /memfd: 开头的内存文件），
// 所以路径必须在启动时缓存，后续一律用缓存值。
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
func CleanupOld() {
	exe, err := selfPath()
	if err != nil {
		return
	}
	os.Remove(exe + ".new")
	os.Remove(exe + ".restore")
	removeStale(exe + ".old")
	// 接力副本正常情况用完就删了，这里兜住中途掉电留下的
	for _, dir := range tmpCandidates() {
		os.Remove(filepath.Join(dir, filepath.Base(exe)+relaySuffix))
	}
}

// removeStale 遗留备份超过10分钟才删
func removeStale(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if time.Since(fi.ModTime()) <= 10*time.Minute {
		log.Println("上一版本备份保留在", path, "需要退回时解压后改名覆盖即可")
		return
	}
	if err := os.Remove(path); err == nil {
		log.Println("已清理旧版本备份", path)
	}
}

// checkELF 检查文件头是不是当前系统的可执行文件
func checkELF(r io.ReaderAt) error {
	head := make([]byte, 4)
	if _, err := r.ReadAt(head, 0); err != nil {
		return errors.New("读取文件头失败：" + err.Error())
	}
	return checkMagic(head)
}

// checkMagic 判断文件头是不是当前系统的可执行文件
func checkMagic(head []byte) error {
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
		return errors.New("下载的文件不是当前系统的可执行文件")
	}
	return nil
}
