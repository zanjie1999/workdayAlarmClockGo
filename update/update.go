/*
 * 自动更新：从 GitHub Release 检查新版本并下载替换自身
 *
 * 运行中的可执行文件在 Linux 上不能直接以写方式打开（ETXTBSY），
 * 但可以删掉目录项后再写同一个路径：代码段和数据段已经映射进内存，
 * 删文件不会影响正在运行的进程，之后 exec 一次自己就完成升级了。
 *
 * 磁盘空间吃紧的设备上，同时存在「旧版 + 新版 + 备份」很容易把分区写满，
 * 所以升级流程改成：
 *  1. 先把当前版本 gzip 压缩备份（优先 /tmp，没有 /tmp 再放程序所在目录），
 *     压缩后通常只有原文件的三分之一左右；
 *  2. 删掉正在运行的旧文件，把空间彻底让出来；
 *  3. 再下载新版本，落到同一个路径上。
 * 下载/校验/替换任何一步失败，都会用备份把旧版本解压回去，不会留下一台没有程序的设备。
 */

package update

import (
	"compress/gzip"
	"encoding/binary"
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
	// 首选的备份目录：大多数 Linux 设备上 /tmp 是 tmpfs，不占用户分区
	preferBackupDir = "/tmp"
	// 备份文件后缀
	backupSuffix = ".old.gz"
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
	// 更新时会先删掉正在运行的文件再写新文件，之后 /proc/self/exe
	// 指向的是一个已经删除的旧 inode，路径不能再信
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

// applyTo 下载并替换指定的文件，单独拆出来方便测试。
//
// 为了省空间，顺序是「压缩备份旧版 → 删掉旧版 → 下载新版」，
// 峰值占用只有一个压缩包 + 一个新文件，而不是「旧版 + 新版 + 副本」。
func applyTo(exe string, p *Plan) error {
	if p == nil || !p.HasUpdate {
		return errors.New("当前已是最新版本")
	}
	if p.Asset == "" {
		return fmt.Errorf("Release 里没有 %s/%s 的可用文件，请手动下载", runtime.GOOS, runtime.GOARCH)
	}
	applyMu.Lock()
	defer applyMu.Unlock()

	// 记住原来的权限位，后面写到同一个路径上时保持一致
	mode := os.FileMode(0755)
	if fi, err := os.Stat(exe); err == nil {
		mode = fi.Mode().Perm()
	}

	// 1. 先把当前版本 gzip 压缩备份出去（优先 /tmp）。
	//    备份不成功就到此为止，绝不能在没有退路的情况下删掉正在运行的程序。
	backup, err := backupCurrent(exe)
	if err != nil {
		return err
	}
	log.Println("当前版本已压缩备份到", backup)

	// 2. 删掉正在运行的旧文件，把空间让给新版本。
	//    代码段已经映射进内存，删掉目录项当前进程照跑，后面再往同一个路径写新文件即可。
	if err := os.Remove(exe); err != nil {
		return errors.New("删除旧版本文件失败（目录可能没有写权限）：" + err.Error())
	}

	// 3. 下载到同目录的临时文件，校验通过再改名成正式文件名。
	//    用临时文件是为了保证正式路径上不会出现半截文件。
	tmp := exe + ".new"
	os.Remove(tmp)
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
		os.Remove(tmp)
		return withRestore(exe, backup, lastErr)
	}

	// 4. 落地：任何一步失败都用备份把旧版本解压回去
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return withRestore(exe, backup, errors.New("设置执行权限失败："+err.Error()))
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return withRestore(exe, backup, errors.New("替换程序失败："+err.Error()))
	}
	// 替换完成后再确认一次，避免把空文件或半截文件留在原路径上
	if err := checkReplaced(exe, p.Size); err != nil {
		os.Remove(exe)
		return withRestore(exe, backup, err)
	}
	log.Println("更新完成，旧版本压缩备份在", backup)
	return nil
}

// withRestore 更新失败时用备份把旧版本解压回原路径，再带着原始错误返回
func withRestore(exe, backup string, cause error) error {
	if err := restoreFrom(exe, backup); err != nil {
		return fmt.Errorf("%w；从备份 %s 恢复也失败了：%v，请手动解压该文件改名回 %s", cause, backup, err, exe)
	}
	return fmt.Errorf("%w；已从备份恢复旧版本", cause)
}

/* ---------------- 压缩备份 / 恢复 ---------------- */

// backupDirs 备份目录候选：优先 /tmp（多数设备上是 tmpfs，不占用户分区），
// 没有 /tmp 或者写不进去时退回程序所在目录
func backupDirs(exe string) []string {
	dirs := []string{preferBackupDir}
	if dir := filepath.Dir(exe); filepath.Clean(dir) != filepath.Clean(preferBackupDir) {
		dirs = append(dirs, dir)
	}
	return dirs
}

// backupPath 备份文件路径，统一用 gzip 压缩，比原来的 .old 全量副本省一大半空间
func backupPath(exe, dir string) string {
	return filepath.Join(dir, filepath.Base(exe)+backupSuffix)
}

// backupCurrent 把正在运行的 exe gzip 压缩备份下来，返回备份文件路径
func backupCurrent(exe string) (string, error) {
	var lastErr error
	for _, dir := range backupDirs(exe) {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		dst := backupPath(exe, dir)
		// 上一轮留下的同名备份先删掉，避免同时存在两份
		os.Remove(dst)
		if err := gzipFile(exe, dst); err != nil {
			lastErr = err
			log.Println("压缩备份到", dst, "失败", err)
			os.Remove(dst)
			continue
		}
		if err := checkBackup(exe, dst); err != nil {
			lastErr = err
			log.Println("备份校验失败", dst, err)
			os.Remove(dst)
			continue
		}
		return dst, nil
	}
	if lastErr == nil {
		lastErr = errors.New("/tmp 和程序目录都不存在或不可写")
	}
	return "", errors.New("备份当前版本失败：" + lastErr.Error())
}

// gzipFile 把 src 压缩写到 dst
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		out.Close()
		return err
	}
	zw.Name = filepath.Base(src)
	zw.ModTime = time.Now()
	buf := make([]byte, 256*1024)
	if _, err := io.CopyBuffer(zw, in, buf); err != nil {
		zw.Close()
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// checkBackup 粗校验备份：能解开、开头是可执行文件、解压后长度和原文件一致
func checkBackup(exe, backup string) error {
	srcInfo, err := os.Stat(exe)
	if err != nil {
		return err
	}
	f, err := os.Open(backup)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("备份文件无法解开：" + err.Error())
	}
	defer zr.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(zr, head); err != nil {
		return errors.New("备份内容不完整：" + err.Error())
	}
	if err := checkMagic(head); err != nil {
		return errors.New("备份内容不是当前系统的可执行文件")
	}
	// gzip 尾部的 ISIZE 记录原文件长度，用它快速排掉写了一半的备份
	if isize, err := gzipISize(f); err == nil && int64(isize) != srcInfo.Size() {
		return fmt.Errorf("备份长度不符，期望 %d 实际 %d", srcInfo.Size(), isize)
	}
	return nil
}

// gzipISize 读 gzip 尾部记录的原文件长度
func gzipISize(f *os.File) (uint32, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Size() < 18 {
		return 0, errors.New("备份文件过小")
	}
	var b [4]byte
	if _, err := f.ReadAt(b[:], fi.Size()-4); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

// restoreFrom 用 gzip 备份把 exe 解压回原路径，更新失败时兜底
func restoreFrom(exe, backup string) error {
	f, err := os.Open(backup)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	tmp := exe + ".restore"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	if _, err := io.CopyBuffer(out, zr, buf); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chmod(tmp, 0755)
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	log.Println("已从备份恢复旧版本到", exe)
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
		return errors.New("下载的文件不是可执行文件")
	}
	return nil
}

// selfPath 当前可执行文件的真实路径（解开软链接）
//
// 结果只在第一次调用时解析并缓存。Linux 的 /proc/self/exe 会跟着文件走：
// 更新流程会先删掉正在运行的程序再写新文件，之后 /proc/self/exe 指向的是
// 一个已经删除的旧 inode，再解析拿到的路径就不可靠了。所以 Init 时会先预热缓存，
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
// 压缩备份只在它已经存在超过10分钟时才删，
// 这样新版本刚替换完就起不来时，还能解压备份退回去。
func CleanupOld() {
	exe, err := selfPath()
	if err != nil {
		return
	}
	os.Remove(exe + ".new")
	os.Remove(exe + ".restore")
	// 老版本升级流程留下的未压缩副本，一并清掉
	removeStale(exe + ".old")
	for _, dir := range backupDirs(exe) {
		removeStale(backupPath(exe, dir))
	}
}

// removeStale 备份放置超过10分钟才删，留着给刚升级完的新版本一个退路
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
