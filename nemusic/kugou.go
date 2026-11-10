/*
 * 酷狗音乐 歌单与播放地址解析
 * 只做「取歌单 id 列表」和「取单曲播放地址」两件事
 * 酷狗播放需要 hash 和 album_id，所以单曲 id 用 hash_albumid 表示
 * zyyme 20261103
 */

package nemusic

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"workdayAlarmClock/conf"

	"github.com/zanjie1999/httpme"
)

const (
	// mobilecdn.kugou.com 的证书对不上，换成同样可用且证书正常的新域名
	kugouSongAPI = "https://mobileservice.kugou.com/api/v3/special/song"
	kugouReferer = "https://www.kugou.com/"
	// kg_mid 是客户端的设备标识，随便一个 32 位 hex 即可
	kugouDefaultMid = "2b1a8f4c9d3e7a6b5c4d3e2f1a0b9c8d"

	// 新版接口都要走网关，并且要带签名
	kugouGateway   = "https://gateway.kugou.com"
	kugouSignSalt  = "OIlwieks28dk2k092lksi2UIkp"
	kugouAppID     = "1005"
	kugouClientVer = "20489"
	// 歌单歌曲，支持 collection_3_xxx 这种新版 id
	kugouListFileAPI = "/pubsongs/v2/get_other_list_file_nofilt"
	// 账号歌单列表，必须带 userid 和 token
	kugouUserListAPI = "/v7/get_all_list"
	// collection_ 前缀，新版歌单 id
	kugouCollectionPrefix = "collection_"
)

// kugouAPIClient 网关接口用的客户端
var kugouAPIClient = &http.Client{Timeout: 20 * time.Second}

// kugouSource 酷狗音乐实现
type kugouSource struct{}

// CachePrefix 酷狗缓存文件前缀
func (kugouSource) CachePrefix() string { return "kg_" }

// kugouSongID 拼酷狗单曲 id，没有专辑号就只用 hash
func kugouSongID(hash string, albumID int64) string {
	if albumID <= 0 {
		return hash
	}
	return hash + "_" + strconv.FormatInt(albumID, 10)
}

// splitKugouID 拆酷狗单曲 id
func splitKugouID(id string) (hash, albumID string) {
	parts := strings.SplitN(id, "_", 2)
	hash = parts[0]
	if len(parts) > 1 {
		albumID = parts[1]
	}
	return hash, albumID
}

// KugouCookieUserID 从 cookie 里取酷狗用户 id，取不到返回空串
func KugouCookieUserID(cookie string) string {
	if v := digitsOnly(cookieValue(cookie, "userid", "uid", "KugooID")); v != "" {
		return v
	}
	// 有些 cookie 把 userid 塞在 KuGoo=... 这种复合字段里
	return digitsOnly(kugouCookieInner(cookie, "userid"))
}

// kugouCookieToken 从 cookie 里取登录 token，账号相关接口都要带
// 官网 cookie 里 token 常常叫 t，也可能藏在 KuGoo=... 复合字段里
func kugouCookieToken(cookie string) string {
	if v := cookieValue(cookie, "token", "t"); v != "" {
		return v
	}
	if v := kugouCookieInner(cookie, "token"); v != "" {
		return v
	}
	return kugouCookieInner(cookie, "t")
}

// kugouTokenCandidates cookie 里可能放 token 的位置，按可信度排序去重
// 复合 KuGoo 字段里的 token 才是账号令牌，顶层的 t 有时是别的东西
func kugouTokenCandidates(cookie string) []string {
	all := []string{
		cookieValue(cookie, "token"),
		kugouCookieInner(cookie, "token"),
		kugouCookieInner(cookie, "t"),
		cookieValue(cookie, "t"),
	}
	out := make([]string, 0, len(all))
	for _, v := range all {
		if v == "" {
			continue
		}
		dup := false
		for _, o := range out {
			if o == v {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

// kugouCookieKeys 列出 cookie 里的字段名，排查登录态用，不输出字段值
func kugouCookieKeys(cookie string) string {
	parts := strings.Split(cookie, ";")
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		key, _, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key != "" {
			keys = append(keys, key)
		}
	}
	return strings.Join(keys, ",")
}

// kugouCookieInner 从 cookie 各字段的值里找 key=value 形式的内层字段
// 有的 cookie 把 userid/token 塞在 KuGoo=... 这种复合字段里，还可能是 URL 编码的
func kugouCookieInner(cookie, key string) string {
	needle := key + "="
	for _, part := range strings.Split(cookie, ";") {
		_, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if decoded, err := url.QueryUnescape(value); err == nil {
			if v := kugouInnerValue(decoded, needle); v != "" {
				return v
			}
		}
		if v := kugouInnerValue(value, needle); v != "" {
			return v
		}
	}
	return ""
}

// kugouInnerValue 从一段 k=v&k2=v2 里取指定 key 的值
func kugouInnerValue(value, needle string) string {
	i := strings.Index(value, needle)
	if i < 0 {
		return ""
	}
	v := value[i+len(needle):]
	if j := strings.IndexAny(v, "&;"); j >= 0 {
		v = v[:j]
	}
	return strings.Trim(strings.TrimSpace(v), `"`)
}

// KugouCookieToken 暴露给路由层判断是否已登录
func KugouCookieToken(cookie string) string { return kugouCookieToken(cookie) }

// kugouSign 网关接口签名：参数按 key 排序拼成 k=v，拼上请求体，前后各加一次盐做 md5
func kugouSign(salt string, params map[string]string, body string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(salt)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(params[k])
	}
	b.WriteString(body)
	b.WriteString(salt)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// kugouAuth 一套客户端身份，不同端拿到的 token 只对自己那套 appid 有效
type kugouAuth struct {
	appID     string
	clientVer string
	salt      string
}

// kugouAuthOptions 标准版、网页版、概念版的 appid/clientver/签名盐
var kugouAuthOptions = []kugouAuth{
	{kugouAppID, kugouClientVer, kugouSignSalt},
	{kugouAppID, "1000", kugouSignSalt},
	{"3116", "11440", "LnT6xpN3khm36zse0QzvmgTZ3waWdRSA"},
}

// kugouGatewayRequest 带公众参数和签名请求网关，返回原始响应
// router 非空时作为 x-router 路由到具体业务域名
func kugouGatewayRequest(method, path, router string, params map[string]string, body string) ([]byte, error) {
	return kugouGatewayRequestAuth(kugouAuthOptions[0], method, path, router, params, body)
}

// kugouGatewayRequestAuth 指定客户端身份的版本
func kugouGatewayRequestAuth(auth kugouAuth, method, path, router string, params map[string]string, body string) ([]byte, error) {
	clientTime := strconv.FormatInt(time.Now().Unix(), 10)
	mid := kugouCookieMid(conf.Cfg.Cookie)
	dfid := kugouCookieDfid(conf.Cfg.Cookie)
	query := map[string]string{
		"appid":      auth.appID,
		"clientver":  auth.clientVer,
		"clienttime": clientTime,
		"dfid":       dfid,
		"mid":        mid,
		"uuid":       "-",
	}
	for k, v := range params {
		query[k] = v
	}
	query["signature"] = kugouSign(auth.salt, query, body)
	target, err := url.Parse(kugouGateway + path)
	if err != nil {
		return nil, err
	}
	q := target.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	target.RawQuery = q.Encode()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", kugouReferer)
	req.Header.Set("User-Agent", "Android9-AndroidPhone-"+auth.clientVer+"-18-0-playlist-wifi")
	// 真实客户端这几个头也带着，少了有的接口会当成未知设备
	req.Header.Set("mid", mid)
	req.Header.Set("dfid", dfid)
	req.Header.Set("clienttime", clientTime)
	if router != "" {
		req.Header.Set("x-router", router)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json;charset=utf-8")
	}
	resp, err := kugouAPIClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("接口状态异常: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// 扫码登录接口，参数和签名在服务端拼好，走反向代理原样透传
const (
	kugouLoginHost = "https://login-user.kugou.com"
	// web 端签名用的盐
	kugouWebSalt = "NVPh5oo715z5DIWAeQlhMDsWXXQV4hwt"
)

var (
	kugouLoginOnce sync.Once
	kugouLoginGuid string
	kugouLoginDev  string
)

// kugouLoginDevice 扫码登录用的设备标识，进程内固定，不要每次变
func kugouLoginDevice() (guid, dev string) {
	kugouLoginOnce.Do(func() {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			// 拿不到随机数就用时间凑，这里只是设备标识
			for i := range b {
				b[i] = byte(time.Now().UnixNano() >> (8 * i))
			}
		}
		kugouLoginGuid = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
		const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		d := make([]byte, 10)
		for i := range d {
			d[i] = letters[int(b[i])%len(letters)]
		}
		kugouLoginDev = string(d)
	})
	return kugouLoginGuid, kugouLoginDev
}

// KugouLoginURL 返回带签名的扫码登录接口地址
// action 为 qrcode 时取二维码，为 check 时按 key 查扫码状态
func KugouLoginURL(action, key string) string {
	guid, dev := kugouLoginDevice()
	params := map[string]string{
		"appid":      kugouAppID,
		"clientver":  kugouClientVer,
		"clienttime": strconv.FormatInt(time.Now().UnixMilli(), 10),
		"dfid":       "-",
		"mid":        kugouDefaultMid,
		"uuid":       guid,
	}
	path := "/v2/qrcode"
	switch action {
	case "check":
		path = "/v2/get_userinfo_qrcode"
		params["plat"] = "4"
		params["srcappid"] = "2919"
		params["qrcode"] = key
		params["dev"] = dev
	default:
		// 二维码用网页端的 appid，扫码后拿到的 token 仍然属于 appid=1005
		params["appid"] = "1014"
		params["type"] = "1"
		params["plat"] = "4"
		params["srcappid"] = "2919"
		params["qrcode_txt"] = "https://h5.kugou.com/apps/loginQRCode/html/index.html?appid=" + kugouAppID + "&"
	}
	params["signature"] = kugouSign(kugouWebSalt, params, "")
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return kugouLoginHost + path + "?" + q.Encode()
}

// kugouCookieMid 从 cookie 里取 kg_mid，没有就用固定值
func kugouCookieMid(cookie string) string {
	if v := cookieValue(cookie, "kg_mid"); v != "" {
		return v
	}
	return kugouDefaultMid
}

// kugouCookieDfid 从 cookie 里取设备指纹 kg_dfid，没有就用 -
func kugouCookieDfid(cookie string) string {
	if v := cookieValue(cookie, "kg_dfid", "dfid"); v != "" {
		return v
	}
	return "-"
}

// KugouMid 暴露给路由层拼请求参数用
func KugouMid(cookie string) string { return kugouCookieMid(cookie) }

func (kugouSource) MusicURL(id, quality string) (string, error) {
	hash, _ := splitKugouID(id)
	if hash == "" {
		return "", fmt.Errorf("歌曲id为空")
	}
	// 这个老接口不需要签名，返回 url 或备用地址
	target := "http://m.kugou.com/app/i/getSongInfo.php?cmd=playInfo&hash=" + url.QueryEscape(hash)
	var j kugouSongInfoResp
	if err := kugouGetWith(target, "https://m.kugou.com/", &j); err != nil {
		return "", err
	}
	if j.URL != "" {
		return j.URL, nil
	}
	// 主地址没给时试备用地址
	for _, u := range kugouBackupURLs(j.Backup) {
		if u != "" {
			return u, nil
		}
	}
	return "", fmt.Errorf("没有拿到播放地址，可能需要VIP或没有版权")
}

type kugouSongInfoResp struct {
	Status   int    `json:"status"`
	ErrCode  int    `json:"errcode"`
	URL      string `json:"url"`
	FileName string `json:"fileName"`
	// backup_url 为空时是 {}，有地址时是数组，只能按原始字节判断
	Backup json.RawMessage `json:"backup_url"`
}

// kugouBackupURLs 备用地址是数组时才解析，空对象直接忽略
func kugouBackupURLs(raw json.RawMessage) []string {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var urls []string
	if err := json.Unmarshal(raw, &urls); err != nil {
		return nil
	}
	return urls
}

func (kugouSource) PlayList(id string) ([]string, []string, string, bool) {
	ids, names, name, err := kugouPlayListIDs(id)
	if err == nil && len(ids) > 0 {
		saveListCache(id, ids)
		return ids, names, name, false
	}
	if err != nil {
		log.Println("酷狗获取歌单出错", err)
	}
	if cached, ok := loadListCache(id); ok {
		return cached, nil, "缓存", true
	}
	if name != "" {
		return []string{}, nil, name, false
	}
	return []string{}, nil, "无法播放", false
}

type kugouSongResp struct {
	Status int `json:"status"`
	Data   struct {
		Total int `json:"total"`
		Info  []struct {
			Hash     string    `json:"hash"`
			AlbumID  flexInt64 `json:"album_id"`
			Filename string    `json:"filename"`
		} `json:"info"`
	} `json:"data"`
}

// kugouPlayListIDs 取歌单名和歌曲列表，歌单名取不到不影响播放
func kugouPlayListIDs(id string) ([]string, []string, string, error) {
	// 账号里的歌单多是新版 collection_ 的 id，老接口只认数字，只能走网关
	if strings.HasPrefix(id, kugouCollectionPrefix) {
		return kugouCollectionListIDs(id)
	}
	// 老接口不返回歌单名，取不到就留空，不要拿 id 拼
	name := ""
	var songs kugouSongResp
	// appid 和 mid 不带会直接返回 Access Deny
	target := kugouSongAPI + "?specialid=" + url.QueryEscape(id) + "&page=1&pagesize=-1&plat=0&version=9108" +
		"&appid=1005&clientver=9108&mid=" + kugouCookieMid(conf.Cfg.Cookie)
	if err := kugouGet(target, &songs); err != nil {
		return nil, nil, name, err
	}
	if len(songs.Data.Info) == 0 {
		return nil, nil, name, fmt.Errorf("歌单没有歌曲")
	}
	ids := make([]string, 0, len(songs.Data.Info))
	names := make([]string, 0, len(songs.Data.Info))
	for _, s := range songs.Data.Info {
		if s.Hash == "" {
			continue
		}
		ids = append(ids, kugouSongID(s.Hash, int64(s.AlbumID)))
		// filename 就是"歌手 - 歌名"，有什么显示什么，不额外拆
		names = append(names, s.Filename)
	}
	return ids, names, name, nil
}

// kugouGet 流式解码酷狗接口
func kugouGet(target string, v interface{}) error {
	return kugouGetWith(target, kugouReferer, v)
}

// kugouGetWith 指定 Referer 的版本，m.kugou.com 与 www.kugou.com 要求不同
func kugouGetWith(target, referer string, v interface{}) error {
	resp, err := httpme.Httpme().GetStream(target, httpme.Header{
		"Referer": referer,
		"Cookie":  conf.Cfg.Cookie,
	})
	if err != nil {
		return err
	}
	defer resp.R.Body.Close()
	if resp.R.StatusCode != http.StatusOK {
		return fmt.Errorf("接口状态异常: %s", resp.R.Status)
	}
	if err := json.NewDecoder(resp.R.Body).Decode(v); err != nil {
		return fmt.Errorf("接口返回解析失败: %w", err)
	}
	return nil
}

// kugouListFileResp 网关歌单歌曲接口的返回，歌曲在 data.songs 里
type kugouListFileResp struct {
	ErrorCode int    `json:"error_code"`
	ErrMsg    string `json:"errmsg"`
	Data      struct {
		Count    int `json:"count"`
		ListInfo struct {
			Name string `json:"name"`
		} `json:"list_info"`
		Songs []struct {
			Hash    string    `json:"hash"`
			Name    string    `json:"name"`
			AlbumID flexInt64 `json:"album_id"`
		} `json:"songs"`
	} `json:"data"`
}

// kugouCollectionParams 新版歌单歌曲接口的参数，begin_idx 是偏移不是页码
func kugouCollectionParams(id string, beginIdx, pagesize int) map[string]string {
	return map[string]string{
		"global_collection_id": id,
		"begin_idx":            strconv.Itoa(beginIdx),
		"pagesize":             strconv.Itoa(pagesize),
		"area_code":            "1",
		"plat":                 "1",
		"type":                 "1",
		"mode":                 "1",
		"personal_switch":      "1",
		"userid":               "0",
		"module":               "CloudMusic",
		"need_sort":            "1",
		"need_rd":              "0",
	}
}

// kugouCollectionPage 取一页新版歌单歌曲
func kugouCollectionPage(id string, beginIdx, pagesize int) (*kugouListFileResp, error) {
	raw, err := kugouGatewayRequest(http.MethodGet, kugouListFileAPI, "", kugouCollectionParams(id, beginIdx, pagesize), "")
	if err != nil {
		return nil, err
	}
	var resp kugouListFileResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("接口返回解析失败: %w", err)
	}
	if resp.ErrorCode != 0 {
		return nil, fmt.Errorf("取歌单歌曲失败: %s", resp.ErrMsg)
	}
	return &resp, nil
}

// KugouCollectionSongs 新版 collection_ 歌单的歌曲，转成旧接口的结构给前端用
func KugouCollectionSongs(id string) ([]byte, error) {
	resp, err := kugouCollectionPage(id, 0, 300)
	if err != nil {
		return nil, err
	}
	info := make([]map[string]interface{}, 0, len(resp.Data.Songs))
	for _, s := range resp.Data.Songs {
		if s.Hash == "" {
			continue
		}
		// 前端按 filename 切分歌手和歌名
		info = append(info, map[string]interface{}{
			"hash":     s.Hash,
			"album_id": strconv.FormatInt(int64(s.AlbumID), 10),
			"filename": s.Name,
		})
	}
	return json.Marshal(map[string]interface{}{
		"status":     1,
		"error_code": 0,
		"data": map[string]interface{}{
			"info":  info,
			"total": resp.Data.Count,
		},
	})
}

// kugouCollectionListIDs 分页取完整个新版歌单
func kugouCollectionListIDs(id string) ([]string, []string, string, error) {
	// 名称从接口的 list_info 里拿，拿不到就留空
	name := ""
	const pagesize = 300
	var ids []string
	var names []string
	for begin := 0; ; begin += pagesize {
		resp, err := kugouCollectionPage(id, begin, pagesize)
		if err != nil {
			return nil, nil, name, err
		}
		if resp.Data.ListInfo.Name != "" {
			name = resp.Data.ListInfo.Name
		}
		for _, s := range resp.Data.Songs {
			if s.Hash == "" {
				continue
			}
			ids = append(ids, kugouSongID(s.Hash, int64(s.AlbumID)))
			names = append(names, s.Name)
		}
		// 这一页没取满或者已经凑够总数就结束
		if len(resp.Data.Songs) < pagesize || len(ids) >= resp.Data.Count {
			break
		}
	}
	if len(ids) == 0 {
		return nil, nil, name, fmt.Errorf("歌单没有歌曲")
	}
	return ids, names, name, nil
}

// kugouUserListResp 账号歌单列表接口的返回，字段名各版本不一致，统一按 map 取
type kugouUserListResp struct {
	Status    int                        `json:"status"`
	ErrorCode int                        `json:"error_code"`
	Data      struct {
		Info []map[string]json.RawMessage `json:"info"`
		List []map[string]json.RawMessage `json:"list"`
	} `json:"data"`
}

// kugouRawString 从 map 里按候选 key 取字符串，数字也能取
func kugouRawString(item map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := item[k]
		if !ok || len(raw) == 0 {
			continue
		}
		s := strings.Trim(string(raw), `"`)
		if s == "" || s == "null" {
			continue
		}
		return s
	}
	return ""
}

// kugouRawInt 从 map 里按候选 key 取整数，取不到当 0
func kugouRawInt(item map[string]json.RawMessage, keys ...string) int64 {
	n, err := strconv.ParseInt(kugouRawString(item, keys...), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// kugouUserListPickState 记住哪套 token/appid 能用，避免每次都试一遍
type kugouUserListPickState struct {
	sync.Mutex
	ready    bool
	tokenIdx int
	authIdx  int
}

var kugouUserListPick kugouUserListPickState

// order 有记录只用记录的那套，没有就全试
func (p *kugouUserListPickState) order(tokenCount, authCount int) (tokens, auths []int) {
	p.Lock()
	defer p.Unlock()
	if p.ready {
		return []int{p.tokenIdx}, []int{p.authIdx}
	}
	for i := 0; i < tokenCount; i++ {
		tokens = append(tokens, i)
	}
	for i := 0; i < authCount; i++ {
		auths = append(auths, i)
	}
	return tokens, auths
}

// remember 记住可用的组合
func (p *kugouUserListPickState) remember(tokenIdx, authIdx int) {
	p.Lock()
	defer p.Unlock()
	if p.ready {
		return
	}
	p.ready = true
	p.tokenIdx = tokenIdx
	p.authIdx = authIdx
	log.Println("酷狗账号接口可用组合: token 候选", tokenIdx, "appid", kugouAuthOptions[authIdx].appID)
}

// kugouUserListRequest 按指定 token 和客户端身份请求一次账号歌单
func kugouUserListRequest(token, userID string, auth kugouAuth, page, pagesize int) ([]byte, int, error) {
	body, err := json.Marshal(struct {
		UserID   string `json:"userid"`
		Token    string `json:"token"`
		TotalVer int    `json:"total_ver"`
		Type     int    `json:"type"`
		Page     int    `json:"page"`
		PageSize int    `json:"pagesize"`
	}{userID, token, 979, 2, page, pagesize})
	if err != nil {
		return nil, 0, err
	}
	raw, err := kugouGatewayRequestAuth(auth, http.MethodPost, kugouUserListAPI, "cloudlist.service.kugou.com", map[string]string{
		"plat":   "1",
		"userid": userID,
		"token":  token,
	}, string(body))
	if err != nil {
		return nil, 0, err
	}
	var head struct {
		ErrorCode int `json:"error_code"`
	}
	_ = json.Unmarshal(raw, &head)
	return raw, head.ErrorCode, nil
}

// kugouUserListJSON 把账号歌单转成旧接口的结构(data.info)，前端不用改
func kugouUserListJSON(raw []byte) ([]byte, error) {
	var resp kugouUserListResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("接口返回解析失败: %w", err)
	}
	items := resp.Data.Info
	if len(items) == 0 {
		items = resp.Data.List
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		id := kugouRawString(item, "global_collection_id", "specialid", "listid", "list_id")
		if id == "" || id == "0" {
			continue
		}
		out = append(out, map[string]interface{}{
			"specialid":   id,
			"specialname": kugouRawString(item, "name", "specialname", "listname"),
			"imgurl":      kugouRawString(item, "pic", "imgurl", "cover"),
			"songcount":   kugouRawInt(item, "songcount", "count", "total", "song_count"),
		})
	}
	if len(out) == 0 {
		// 拿不到歌单多半是字段对不上，把返回打到日志里方便排查
		head := raw
		if len(head) > 300 {
			head = head[:300]
		}
		log.Println("酷狗歌单列表为空，接口返回", string(head))
	}
	return json.Marshal(map[string]interface{}{
		"status": 1,
		"data": map[string]interface{}{
			"info":  out,
			"total": len(out),
		},
	})
}

// KugouUserPlayList 取账号歌单列表，输出结构和旧接口一致(data.info)，前端不用改
// userID 传空时用 cookie 里的
// cookie 里能当 token 的值不止一个，官方客户端又有标准版和概念版两套 appid，
// 不同端拿到的 token 只对自己那套有效，所以这里逐个试，成功后记住
func KugouUserPlayList(cookie, userID string, page, pagesize int) ([]byte, error) {
	if userID == "" {
		userID = KugouCookieUserID(cookie)
	}
	userID = digitsOnly(userID)
	if userID == "" {
		return nil, fmt.Errorf("cookie 里没有 userid 或 KugooID，当前字段有 %s", kugouCookieKeys(cookie))
	}
	tokens := kugouTokenCandidates(cookie)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("cookie 里没有 token 或 t，当前字段有 %s", kugouCookieKeys(cookie))
	}
	if page <= 0 {
		page = 1
	}
	if pagesize <= 0 {
		pagesize = 50
	}
	tokenOrder, authOrder := kugouUserListPick.order(len(tokens), len(kugouAuthOptions))
	var lastErr error
	var tried []string
	for _, ti := range tokenOrder {
		for _, ai := range authOrder {
			raw, code, err := kugouUserListRequest(tokens[ti], userID, kugouAuthOptions[ai], page, pagesize)
			if err != nil {
				lastErr = err
				tried = append(tried, fmt.Sprintf("token%d/appid%s 请求失败", ti, kugouAuthOptions[ai].appID))
				continue
			}
			if code != 0 {
				lastErr = fmt.Errorf("错误码 %d", code)
				tried = append(tried, fmt.Sprintf("token%d/appid%s=%d", ti, kugouAuthOptions[ai].appID, code))
				continue
			}
			kugouUserListPick.remember(ti, ai)
			return kugouUserListJSON(raw)
		}
	}
	return nil, fmt.Errorf("获取歌单列表失败(%s): %v", strings.Join(tried, ","), lastErr)
}

var (
	kugouNumericRe = regexp.MustCompile(`^\d+$`)
	kugouSpecialRe = regexp.MustCompile(`(?:special/single/|specialid=)(\d+)`)
	kugouHashRe    = regexp.MustCompile(`^[0-9a-fA-F]{32}(_\d+)?$`)
)

func (kugouSource) ParsePlaylistID(raw string) string {
	if kugouNumericRe.MatchString(raw) {
		return raw
	}
	if m := kugouSpecialRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}

func (kugouSource) ParseSongID(raw string) string {
	if kugouHashRe.MatchString(raw) {
		return raw
	}
	if i := strings.Index(raw, "hash="); i >= 0 {
		rest := raw[i+len("hash="):]
		if j := strings.IndexAny(rest, "&#"); j >= 0 {
			rest = rest[:j]
		}
		rest = strings.TrimSpace(rest)
		if kugouHashRe.MatchString(rest) {
			return rest
		}
	}
	return raw
}
