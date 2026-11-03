/*
 * 简易WebDAV支持
 * zyyme 20261103
 */

package router

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// http.StatusMultiStatus 的字符串形式，部分客户端只认这个
const davAllowMethods = "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, MKCOL, PROPPATCH, LOCK, UNLOCK, MOVE, COPY"

type webdavHandler struct {
	// 目录的绝对路径，注册时快照，与 StaticFS 使用同一个目录
	base string
	// 挂载点，比如 /music，用于解析 MOVE 的 Destination
	route string
}

// RegisterWebDAV 为 route(如 "/music") 挂上最小 WebDAV 能力。
// dir 为空或无效时不注册；GET/HEAD 仍由 StaticFS 提供，这里不动。
func RegisterWebDAV(group *gin.RouterGroup, route, dir string) {
	if dir == "" {
		return
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		log.Println("webdav: 目录无效", dir, err)
		return
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	route = strings.TrimSuffix(route, "/")
	h := &webdavHandler{base: base, route: route}
	// 通配路径先注册，再补精确路径，避免客户端不带尾斜杠时依赖重定向
	group.Handle(http.MethodOptions, route+"/*filepath", h.options)
	group.Handle("PROPFIND", route+"/*filepath", h.propfind)
	group.Handle("PROPPATCH", route+"/*filepath", h.proppatch)
	group.Handle(http.MethodPut, route+"/*filepath", h.put)
	group.Handle(http.MethodDelete, route+"/*filepath", h.delete)
	group.Handle("MKCOL", route+"/*filepath", h.mkcol)
	group.Handle("MOVE", route+"/*filepath", h.move)
	group.Handle("COPY", route+"/*filepath", h.copy)
	group.Handle("LOCK", route+"/*filepath", h.lock)
	group.Handle("UNLOCK", route+"/*filepath", h.unlock)
	group.Handle(http.MethodOptions, route, h.options)
	group.Handle("PROPFIND", route, h.propfind)
	group.Handle("COPY", route, h.copy)
}

// checkBase 确认 base 目录存在，不存在时直接回 404(比如非 Android 上的 /sdcard)
func (h *webdavHandler) checkBase(c *gin.Context) bool {
	info, err := os.Stat(h.base)
	if err != nil || !info.IsDir() {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	return true
}

// safeTarget 把 URL 子路径解析成 base 内的绝对路径，越界返回 false
func (h *webdavHandler) safeTarget(rel string) (string, bool) {
	// 先归一化掉 .. ，再用 filepath.Rel 二次确认，兼容 Windows 盘符与分隔符
	clean := path.Clean("/" + filepath.ToSlash(rel))
	target := filepath.Join(h.base, filepath.FromSlash(clean))
	toBase, err := filepath.Rel(h.base, target)
	if err != nil || toBase == ".." || strings.HasPrefix(toBase, ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

// resolve 解析请求路径，越界回 403 且不回显真实路径
func (h *webdavHandler) resolve(c *gin.Context) (string, bool) {
	target, ok := h.safeTarget(c.Param("filepath"))
	if !ok {
		log.Println("webdav: 越界请求", c.Param("filepath"))
		c.AbortWithStatus(http.StatusForbidden)
		return "", false
	}
	return target, true
}

// destination 解析 MOVE/COPY 的 Destination 头，必须是本挂载点内的路径
func (h *webdavHandler) destination(c *gin.Context) (string, bool) {
	raw := c.GetHeader("Destination")
	if raw == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return "", false
	}
	p := u.Path
	switch {
	case p == h.route:
		p = "/"
	case strings.HasPrefix(p, h.route+"/"):
		p = p[len(h.route):]
	default:
		log.Println("webdav: Destination 不在挂载点内", raw)
		c.AbortWithStatus(http.StatusForbidden)
		return "", false
	}
	target, ok := h.safeTarget(p)
	if !ok {
		log.Println("webdav: Destination 越界", raw)
		c.AbortWithStatus(http.StatusForbidden)
		return "", false
	}
	return target, true
}

// options 声明支持的方法，资源管理器据此判断可写
func (h *webdavHandler) options(c *gin.Context) {
	c.Header("DAV", "1, 2")
	c.Header("MS-Author-Via", "DAV")
	c.Header("Allow", davAllowMethods)
	c.Status(http.StatusOK)
}

type davEntry struct {
	href    string
	name    string
	isDir   bool
	size    int64
	modTime time.Time
	ctype   string
	etag    string
}

func newDavEntry(href, name string, info os.FileInfo) davEntry {
	entry := davEntry{
		href:    href,
		name:    name,
		isDir:   info.IsDir(),
		size:    info.Size(),
		modTime: info.ModTime(),
		ctype:   "application/octet-stream",
		etag:    fmt.Sprintf(`"%x-%x"`, info.Size(), info.ModTime().UnixNano()),
	}
	if !entry.isDir {
		if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
			entry.ctype = t
		}
	} else {
		entry.ctype = "httpd/unix-directory"
	}
	return entry
}

// propfind 列举目录，Depth 只支持 0 和 1，infinity 按 1 处理避免整树扫描
func (h *webdavHandler) propfind(c *gin.Context) {
	if !h.checkBase(c) {
		return
	}
	target, ok := h.resolve(c)
	if !ok {
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	// href 基于请求路径拼，自动带上 urlPrefix
	reqPath := c.Request.URL.Path
	if reqPath == "" {
		reqPath = "/"
	}
	dirHref := strings.TrimSuffix(reqPath, "/") + "/"
	selfHref := reqPath
	if info.IsDir() {
		selfHref = dirHref
	}
	entries := []davEntry{newDavEntry(selfHref, info.Name(), info)}
	if strings.TrimSpace(c.GetHeader("Depth")) != "0" && info.IsDir() {
		if children, err := os.ReadDir(target); err == nil {
			for _, child := range children {
				childInfo, err := child.Info()
				if err != nil {
					log.Println("webdav: 读取条目失败", child.Name(), err)
					continue
				}
				entries = append(entries, newDavEntry(dirHref+child.Name(), child.Name(), childInfo))
			}
		} else {
			log.Println("webdav: 读取目录失败", target, err)
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<D:multistatus xmlns:D="DAV:">`)
	for _, entry := range entries {
		b.WriteString(`<D:response><D:href>`)
		escapeXML(&b, (&url.URL{Path: entry.href}).EscapedPath())
		b.WriteString(`</D:href><D:propstat><D:prop><D:displayname>`)
		escapeXML(&b, entry.name)
		b.WriteString(`</D:displayname><D:getlastmodified>`)
		escapeXML(&b, entry.modTime.UTC().Format(http.TimeFormat))
		b.WriteString(`</D:getlastmodified><D:getetag>`)
		escapeXML(&b, entry.etag)
		b.WriteString(`</D:getetag>`)
		if entry.isDir {
			b.WriteString(`<D:resourcetype><D:collection/></D:resourcetype>`)
		} else {
			b.WriteString(`<D:resourcetype/><D:getcontentlength>`)
			b.WriteString(strconv.FormatInt(entry.size, 10))
			b.WriteString(`</D:getcontentlength><D:getcontenttype>`)
			escapeXML(&b, entry.ctype)
			b.WriteString(`</D:getcontenttype>`)
		}
		b.WriteString(`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
	}
	b.WriteString(`</D:multistatus>`)
	c.Header("Content-Type", "application/xml; charset=utf-8")
	c.String(http.StatusMultiStatus, b.String())
}

// put 上传或覆盖；先写同目录临时文件再改名，避免播放器读到半截文件
func (h *webdavHandler) put(c *gin.Context) {
	if !h.checkBase(c) {
		return
	}
	target, ok := h.resolve(c)
	if !ok {
		return
	}
	status := http.StatusCreated
	if info, err := os.Stat(target); err == nil {
		if info.IsDir() {
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}
		status = http.StatusNoContent
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		// 父路径被文件占了之类，按规范回 409
		log.Println("webdav: 创建目录失败", dir, err)
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	tmp, err := os.CreateTemp(dir, ".webdav-*.part")
	if err != nil {
		log.Println("webdav: 创建临时文件失败", dir, err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()
	if _, err := io.Copy(tmp, c.Request.Body); err != nil {
		log.Println("webdav: 写入失败", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		log.Println("webdav: 关闭临时文件失败", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpName, target); err != nil {
		log.Println("webdav: 保存失败", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	log.Println("webdav: 已保存", target)
	c.Status(status)
}

// delete 删除文件或目录(目录递归)
func (h *webdavHandler) delete(c *gin.Context) {
	if !h.checkBase(c) {
		return
	}
	target, ok := h.resolve(c)
	if !ok {
		return
	}
	if target == h.base {
		// 别把整个目录端了，清缓存有专门的接口
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if info.IsDir() {
		err = os.RemoveAll(target)
	} else {
		err = os.Remove(target)
	}
	if err != nil {
		log.Println("webdav: 删除失败", target, err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	log.Println("webdav: 已删除", target)
	c.Status(http.StatusNoContent)
}

// mkcol 新建目录
func (h *webdavHandler) mkcol(c *gin.Context) {
	if !h.checkBase(c) {
		return
	}
	target, ok := h.resolve(c)
	if !ok {
		return
	}
	if _, err := os.Stat(target); err == nil {
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}
	parent := filepath.Dir(target)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	if err := os.Mkdir(target, os.ModePerm); err != nil {
		log.Println("webdav: 创建目录失败", target, err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusCreated)
}

// move 重命名或移动。Windows 资源管理器新建文件夹、改名都靠它。
func (h *webdavHandler) move(c *gin.Context) {
	if !h.checkBase(c) {
		return
	}
	src, ok := h.resolve(c)
	if !ok {
		return
	}
	dst, ok := h.destination(c)
	if !ok {
		return
	}
	if src == h.base || dst == h.base {
		// 挂载点本身不搬
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if _, err := os.Stat(src); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	status := http.StatusCreated
	if _, err := os.Stat(dst); err == nil {
		if strings.EqualFold(strings.TrimSpace(c.GetHeader("Overwrite")), "F") {
			c.AbortWithStatus(http.StatusPreconditionFailed)
			return
		}
		status = http.StatusNoContent
	}
	err := os.Rename(src, dst)
	if err != nil {
		// 目标是已存在的目录之类，删掉再来一次
		if _, statErr := os.Stat(dst); statErr == nil {
			if removeErr := os.RemoveAll(dst); removeErr == nil {
				err = os.Rename(src, dst)
			}
		}
	}
	if err != nil {
		log.Println("webdav: 移动失败", src, dst, err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	log.Println("webdav: 已移动", src, "->", dst)
	c.Status(status)
}

// copy 复制文件或目录。Depth 只支持 0 和 infinity，传 1 按规范回 400。
func (h *webdavHandler) copy(c *gin.Context) {
	if !h.checkBase(c) {
		return
	}
	src, ok := h.resolve(c)
	if !ok {
		return
	}
	dst, ok := h.destination(c)
	if !ok {
		return
	}
	if src == h.base || dst == h.base {
		// 挂载点本身不复制
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	depth := strings.ToLower(strings.TrimSpace(c.GetHeader("Depth")))
	switch depth {
	case "", "infinity":
		depth = "infinity"
	case "0":
	default:
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	// 目标就是源本身或源的祖先目录时，覆盖会先删掉源，直接拒绝
	if rel, err := filepath.Rel(dst, src); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if srcInfo.IsDir() {
		// 目标落在源目录内部会无限自我复制，直接拒绝
		if rel, err := filepath.Rel(src, dst); err == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}
	status := http.StatusCreated
	if _, err := os.Stat(dst); err == nil {
		if strings.EqualFold(strings.TrimSpace(c.GetHeader("Overwrite")), "F") {
			c.AbortWithStatus(http.StatusPreconditionFailed)
			return
		}
		// 覆盖时先清掉旧目标，避免残留旧目录里的多余文件
		if err := os.RemoveAll(dst); err != nil {
			log.Println("webdav: 覆盖前清理失败", dst, err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		status = http.StatusNoContent
	}
	if srcInfo.IsDir() {
		err = copyTree(src, dst, depth != "0")
	} else {
		err = copyFile(src, dst)
	}
	if err != nil {
		log.Println("webdav: 复制失败", src, dst, err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	log.Println("webdav: 已复制", src, "->", dst)
	c.Status(status)
}

// copyFile 流式复制单个文件，先写同目录临时文件再改名，避免播放器读到半截文件
func copyFile(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".webdav-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		tmp.Close()
		if !done {
			os.Remove(tmpName)
		}
	}()
	// io.Copy 优先走 copy_file_range，不支持时退化成 32KB 池化缓冲，全程不占大块堆内存
	if _, err := io.Copy(tmp, sf); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	done = true
	// 尽力保留源文件权限，FUSE 上失败也不影响内容
	_ = os.Chmod(dst, srcInfo.Mode().Perm())
	return nil
}

// copyTree 递归复制目录；recurse 为 false 时只建目录本身(Depth: 0)。
// 用 ReadDir(64) 分批读，内存占用与目录条目数无关。
func copyTree(src, dst string, recurse bool) error {
	if err := os.MkdirAll(dst, os.ModePerm); err != nil {
		return err
	}
	if !recurse {
		return nil
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(64)
		for _, entry := range entries {
			srcChild := filepath.Join(src, entry.Name())
			dstChild := filepath.Join(dst, entry.Name())
			if entry.IsDir() {
				if err := copyTree(srcChild, dstChild, true); err != nil {
					return err
				}
				continue
			}
			if err := copyFile(srcChild, dstChild); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// lock 不做真实锁，只返回一个假 token。资源管理器/Finder 写入前会发 LOCK，
// 直接回 501 的话挂上去只能读不能写。
func (h *webdavHandler) lock(c *gin.Context) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		log.Println("webdav: 生成锁 token 失败", err)
	}
	token := "opaquelocktoken:" + hex.EncodeToString(buf)
	c.Header("Lock-Token", "<"+token+">")
	body := `<?xml version="1.0" encoding="utf-8"?><D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock>` +
		`<D:locktype><D:write/></D:locktype><D:lockscope><D:exclusive/></D:lockscope><D:depth>infinity</D:depth>` +
		`<D:locktoken><D:href>` + token + `</D:href></D:locktoken></D:activelock></D:lockdiscovery></D:prop>`
	c.Header("Content-Type", "application/xml; charset=utf-8")
	c.String(http.StatusOK, body)
}

// unlock 配合假的 LOCK，直接回成功
func (h *webdavHandler) unlock(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// proppatch 不支持改属性，返回空 propstat 让客户端继续
func (h *webdavHandler) proppatch(c *gin.Context) {
	href := (&url.URL{Path: c.Request.URL.Path}).EscapedPath()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<D:multistatus xmlns:D="DAV:"><D:response><D:href>`)
	escapeXML(&b, href)
	b.WriteString(`</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)
	c.Header("Content-Type", "application/xml; charset=utf-8")
	c.String(http.StatusMultiStatus, b.String())
}

func escapeXML(b *strings.Builder, s string) {
	_ = xml.EscapeText(b, []byte(s))
}
