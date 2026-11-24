//go:build linux

package update

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	// installVerb 接力进来的那一份接管更新时的启动参数，后面跟真实路径和原有参数
	installVerb = "install"
	// memfdHeadroom 走内存路线时额外要求的内存余量，别把设备挤到 OOM
	memfdHeadroom = 16 << 20
)

var (
	// errNoMemfd 内核没有 memfd_create（3.17 之前的内核，或者被 seccomp 拦了）。
	// 这不是错误，只是这条路线走不通，换临时目录。
	errNoMemfd = errors.New("内核不支持 memfd_create")
	// errNoTmpRelay 临时目录用不了（不存在、没写权限、挂载了 noexec）
	errNoTmpRelay = errors.New("临时目录接力用不了")
)

// HandleInstall 处理「接力接管更新」的启动参数，由 main 在最前面调用。
// 返回 true 表示这次启动就是更新用的，main 不该再往下跑。
func HandleInstall(args []string) bool {
	if len(args) < 3 || args[1] != installVerb {
		return false
	}
	real := args[2]
	orig := args[3:]
	if err := installSelf(real, orig); err != nil {
		log.Println("更新失败：", err)
		// 走到这里磁盘上通常还是旧版本，尽量把它拉起来，别让设备停在这
		if execErr := syscall.Exec(real, append([]string{real}, orig...), os.Environ()); execErr != nil {
			log.Println("程序起不来了，请手动下载新版本：", RepoURL()+"/releases", execErr)
		}
	}
	return true
}

// applyViaRelay 用「接力」的方式更新：把新版本放到内存或临时目录里，从那里 exec 一份自己，
// 让旧文件的引用先归零，再由那一份把自己写回真实路径。
// 成功时进程会被新版本接管，不会返回；
// 返回 errNoRelay 表示这里没有可用的接力方式，调用方应该退回旁路方案。
func applyViaRelay(t target, p *Plan) error {
	// 接力路线都是「先删旧再写新」，所以只要「当前可用 + 旧文件大小」够就行
	if msg := t.notEnough(t.free + uint64(t.size)); msg != "" {
		return errors.New(msg)
	}

	// 一、内存文件：转手之后连痕迹都不留，最省事
	if why := memfdUsable(p.Size); why != "" {
		log.Println("不走内存更新：", why)
	} else {
		err := applyInMemory(t, p)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errNoMemfd) {
			return err
		}
		log.Println("内存文件用不了，改用/tmp接力：", err)
	}

	// 二、临时目录：内核太老没有 memfd 时的替代，/tmp 在 tmpfs 上时同样不占程序分区
	dir, why := tmpRelayDir()
	if why != "" {
		return fmt.Errorf("%w：%s", errNoRelay, why)
	}
	if free, err := freeSpace(dir); err != nil {
		log.Println("查询", dir, "的可用空间失败，跳过检查", err)
	} else if t.need > 0 && free < t.need {
		// 临时目录本身也要放得下这份新版本；它和程序在同一分区时，这条就是加倍空间的要求
		return fmt.Errorf("空间不足，本次不更新：%s 可用 %s，新版本需要 %s",
			dir, humanSize(free), humanSize(t.need))
	}
	err := applyTmp(t, p, dir)
	if err == nil {
		return nil
	}
	if errors.Is(err, errNoTmpRelay) {
		return fmt.Errorf("%w：%v", errNoRelay, err)
	}
	return err
}

// applyTmp 把新版本下载到临时目录，从那里 exec 一份自己来接力。
// 临时目录在别的分区（tmpfs）时，下载过程完全不占程序所在分区。
func applyTmp(t target, p *Plan, dir string) error {
	relay := filepath.Join(dir, filepath.Base(t.exe)+relaySuffix)
	os.Remove(relay)
	// 读写方式打开：下载完要 ReadAt 校验文件头，只写的 fd 读不了
	f, err := os.OpenFile(relay, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0755)
	if err != nil {
		return fmt.Errorf("%w：%v", errNoTmpRelay, err)
	}
	size, err := downloadPlan(p, f)
	if err != nil {
		f.Close()
		os.Remove(relay)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(relay)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(relay)
		return err
	}
	if err := os.Chmod(relay, 0755); err != nil {
		os.Remove(relay)
		return errors.New("设置执行权限失败：" + err.Error())
	}
	log.Println("新版本已下载到", relay, humanSize(uint64(size)))

	// 从临时目录接手：把真实路径和原有参数带过去，PID 不变
	log.Println("即将从临时目录切换到新版本：", t.exe)
	argv := append([]string{t.exe, installVerb, t.exe}, os.Args[1:]...)
	err = syscall.Exec(relay, argv, os.Environ())
	// 走到这里说明临时目录执行不了（挂载了 noexec 之类）
	os.Remove(relay)
	return fmt.Errorf("%w：%v", errNoTmpRelay, err)
}

// tmpRelayDir 挑一个能放接力副本的临时目录，返回空字符串表示没有
func tmpRelayDir() (string, string) {
	for _, dir := range tmpCandidates() {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir, ""
		}
	}
	return "", "/tmp 和 $TMPDIR 都不可用"
}

// memfdUsable 判断能不能走内存路线，返回空串表示可以，否则是不能走的原因
func memfdUsable(size int64) string {
	if size <= 0 {
		size = 16 << 20
	}
	avail, err := memAvailable()
	if err != nil {
		// 读不到内存信息就不拦，真不够了下载会失败，反正还没动磁盘
		return ""
	}
	if avail < uint64(size)+memfdHeadroom {
		return fmt.Sprintf("可用内存 %s，装不下 %s 的新版本", humanSize(avail), humanSize(uint64(size)))
	}
	return ""
}

// memAvailable 读 /proc/meminfo 里的 MemAvailable
func memAvailable() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			break
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	return 0, errors.New("meminfo 里没有 MemAvailable")
}

// applyInMemory 把新版本下载进 memfd，然后从内存 exec 出去接管更新。
// 成功时不会返回；返回 error 表示磁盘上的东西一个字节都没动过。
func applyInMemory(t target, p *Plan) error {
	fd, err := unix.MemfdCreate("workdayAlarmClock-"+p.Latest, 0)
	if err != nil {
		// ENOSYS 是内核太老，EPERM/EINVAL 可能是 seccomp 或安全模块拦的，
		// 都当作「这条路走不通」交给调用方换临时目录
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w：%v", errNoMemfd, err)
		}
		return errors.New("创建内存文件失败：" + err.Error())
	}
	// 不带 CLOEXEC，exec 之后 fd 还在，里面就是新版本
	f := os.NewFile(uintptr(fd), "/memfd:workdayAlarmClock")
	defer f.Close()

	size, err := downloadPlan(p, f)
	if err != nil {
		return err
	}
	log.Println("新版本已下载到内存", humanSize(uint64(size)))

	// 从内存接手：把真实路径和原有参数带过去，PID 不变
	log.Println("即将从内存切换到新版本：", t.exe)
	argv := append([]string{t.exe, installVerb, t.exe}, os.Args[1:]...)
	err = syscall.Exec(fmt.Sprintf("/proc/self/fd/%d", fd), argv, os.Environ())
	// 走到这里说明从内存执行不了（/proc 没挂、内核禁止执行 memfd 之类）
	log.Println("从内存执行失败：", err, "，改为把内存里的新版本写到磁盘")

	if msg := t.notEnough(t.free); msg != "" {
		return errors.New(msg)
	}
	out, err := createNew(t)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		out.Close()
		os.Remove(out.Name())
		return err
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		os.Remove(out.Name())
		return errors.New("写入新版本失败：" + err.Error())
	}
	return finishBeside(t, out, size)
}

// installSelf 把自己（刚刚接力进来的新版本）写到 real 上，然后 exec 回去。
// 走到这里时旧文件已经没有任何进程持有，删掉就是真的释放空间。
func installSelf(real string, orig []string) error {
	// 接力进来的那一份就是新版本，从 /proc/self/exe 读回来（内存文件也能读）
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return errors.New("读取接力进来的自己失败：" + err.Error())
	}
	defer self.Close()
	fi, err := self.Stat()
	if err != nil {
		return errors.New("读取接力进来的自己失败：" + err.Error())
	}
	size := fi.Size()

	mode := os.FileMode(0755)
	var oldSize int64
	if ofi, err := os.Stat(real); err == nil {
		mode = ofi.Mode().Perm()
		oldSize = ofi.Size()
	}
	// 最后一次能反悔的地方：旧文件还没删，空间不够就直接回旧版本
	need := uint64(size) + uint64(size/spaceSlack)
	if free, err := freeSpace(filepath.Dir(real)); err == nil {
		if free+uint64(oldSize) < need {
			return fmt.Errorf("空间不足：释放旧版本后可用 %s，新版本需要 %s",
				humanSize(free+uint64(oldSize)), humanSize(need))
		}
	} else {
		log.Println("查询可用空间失败，跳过检查", err)
	}

	// 旧文件此刻已经没有任何进程持有，删掉立刻腾出空间
	if err := os.Remove(real); err != nil {
		return errors.New("删除旧版本失败（目录可能没有写权限）：" + err.Error())
	}
	log.Println("已删除旧版本，空间已释放")

	tmp := real + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return errors.New("创建新文件失败：" + err.Error())
	}
	if _, err := self.Seek(0, io.SeekStart); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := io.Copy(out, self); err != nil {
		out.Close()
		os.Remove(tmp)
		return errors.New("写入新版本失败：" + err.Error())
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
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return errors.New("设置执行权限失败：" + err.Error())
	}
	if err := os.Rename(tmp, real); err != nil {
		os.Remove(tmp)
		return errors.New("替换程序失败：" + err.Error())
	}
	// 目录项也要刷盘，否则 rename 之后掉电可能找不到文件
	syncDir(filepath.Dir(real))
	if err := checkReplaced(real, size); err != nil {
		return err
	}
	// 临时目录里那份接力副本用完就删（内存里的那份会随 exec 自动消失）
	removeRelaySelf()

	log.Println("更新完成，正在启动新版本：", real)
	return syscall.Exec(real, append([]string{real}, orig...), os.Environ())
}

// removeRelaySelf 删掉临时目录里的接力副本。
// 只认我们自己起的名字，避免被手动以安装模式运行时把真程序删了。
func removeRelaySelf() {
	p, err := os.Readlink("/proc/self/exe")
	if err != nil || !strings.HasSuffix(p, relaySuffix) {
		return
	}
	if err := os.Remove(p); err == nil {
		log.Println("已清理临时副本", p)
	}
}

// syncDir 把目录项刷到磁盘
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
