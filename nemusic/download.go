/*
 * 音频缓存下载工具
 * zyyme 20261103
 */

package nemusic

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/zanjie1999/httpme"
)

// downloadTo 把 url 流式写到本地文件，失败时删掉半成品
func downloadTo(url, path string) error {
	return downloadToWithHeader(url, path, nil)
}

// downloadToWithHeader 同 downloadTo，可以带请求头，部分平台取流需要 Referer
func downloadToWithHeader(url, path string, header httpme.Header) error {
	args := []interface{}{}
	if header != nil {
		args = append(args, header)
	}
	resp, err := httpme.GetStream(url, args...)
	if err != nil {
		return err
	}
	defer resp.R.Body.Close()
	if resp.R.StatusCode != http.StatusOK {
		return fmt.Errorf("下载状态异常: %s", resp.R.Status)
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, resp.R.Body)
	closeErr := file.Close()
	if err != nil {
		os.Remove(path)
		return err
	}
	if closeErr != nil {
		os.Remove(path)
		return closeErr
	}
	return nil
}
