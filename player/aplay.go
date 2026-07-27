//go:build linux

package player

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/hajimehoshi/go-mp3"
	"github.com/zanjie1999/httpme"
)

var (
	pcmStreamMu sync.Mutex
	pcmStream   io.ReadCloser
)

const pcmNetworkRetryLimit = 2

type reconnectingHTTPStream struct {
	mu            sync.Mutex
	url           string
	body          io.ReadCloser
	offset        int64
	contentLength int64
	retries       int
	pendingErr    error
	closed        bool
}

func newReconnectingHTTPStream(url string) (*reconnectingHTTPStream, error) {
	resp, err := requestMP3Stream(url, 0)
	if err != nil {
		return nil, err
	}
	return &reconnectingHTTPStream{
		url:           url,
		body:          resp.Body,
		contentLength: responseTotalLength(resp),
	}, nil
}

func (s *reconnectingHTTPStream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, io.ErrClosedPipe
		}
		body := s.body
		readErr := s.pendingErr
		s.pendingErr = nil
		s.mu.Unlock()

		if readErr == nil {
			n, err := body.Read(p)
			s.mu.Lock()
			s.offset += int64(n)
			if n > 0 && err != nil {
				s.pendingErr = err
			}
			closed := s.closed
			s.mu.Unlock()
			if n > 0 {
				return n, nil
			}
			if err == nil {
				return 0, nil
			}
			if closed {
				return 0, err
			}
			readErr = err
		}

		if readErr == io.EOF && s.downloadComplete() {
			return 0, io.EOF
		}
		for {
			if !s.canReconnect() {
				return 0, readErr
			}
			if err := s.reconnect(readErr); err != nil {
				readErr = err
				continue
			}
			break
		}
	}
}

func (s *reconnectingHTTPStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	body := s.body
	s.mu.Unlock()
	return body.Close()
}

func (s *reconnectingHTTPStream) downloadComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contentLength >= 0 && s.offset >= s.contentLength
}

func (s *reconnectingHTTPStream) canReconnect() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.retries < pcmNetworkRetryLimit
}

func (s *reconnectingHTTPStream) reconnect(cause error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return io.ErrClosedPipe
	}
	s.retries++
	attempt := s.retries
	offset := s.offset
	s.mu.Unlock()

	log.Printf("网络播放中断，已读取 %d 字节，重连 (%d/%d): %v", offset, attempt, pcmNetworkRetryLimit, cause)
	resp, err := requestMP3Stream(s.url, offset)
	if err != nil {
		return err
	}
	partial := resp.StatusCode == http.StatusPartialContent
	if partial && !strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes "+strconv.FormatInt(offset, 10)+"-") {
		resp.Body.Close()
		return fmt.Errorf("unexpected content range: %s", resp.Header.Get("Content-Range"))
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		resp.Body.Close()
		return io.ErrClosedPipe
	}
	oldBody := s.body
	s.body = resp.Body
	s.pendingErr = nil
	if total := responseTotalLength(resp); total >= 0 {
		s.contentLength = total
	}
	s.mu.Unlock()
	oldBody.Close()

	if !partial && offset > 0 {
		if _, err = io.CopyN(io.Discard, resp.Body, offset); err != nil {
			return fmt.Errorf("skip %d downloaded bytes: %w", offset, err)
		}
	}
	return nil
}

func defaultShellPlayer() string {
	return "aplay"
}

func usePCMPlayer() bool {
	name := filepath.Base(ShellPlayer)
	return name == "aplay" || name == "kindle"
}

func isKindle() bool {
	return filepath.Base(ShellPlayer) == "kindle"
}

func pcmURL(url string) error {
	stream, err := openMP3(url)
	if err != nil {
		return err
	}
	if playbackCanceled(url) {
		stream.Close()
		return fmt.Errorf("playback canceled")
	}
	if !setPCMStream(url, stream) {
		stream.Close()
		return fmt.Errorf("playback canceled")
	}
	defer func() {
		stream.Close()
		clearPCMStream(stream)
	}()
	if playbackCanceled(url) {
		return fmt.Errorf("playback canceled")
	}

	decoder, err := mp3.NewDecoder(stream)
	if err != nil {
		return fmt.Errorf("decode mp3: %w", err)
	}
	cmd := pcmCommand(decoder.SampleRate())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = startUnixCmd(cmd); err != nil {
		return err
	}
	if IsPaused {
		_ = signalUnixCmd(syscall.SIGSTOP)
	}

	_, copyErr := io.Copy(stdin, decoder)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	clearUnixCmd(cmd)
	if playbackCanceled(url) {
		return fmt.Errorf("playback canceled")
	}
	if waitErr != nil {
		return waitErr
	}
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func playbackCanceled(url string) bool {
	return (IsStop && !IsPlayWeather) || NowUrl != url
}

func pcmCommand(sampleRate int) *exec.Cmd {
	rate := strconv.Itoa(sampleRate)
	if isKindle() {
		return exec.Command("/usr/bin/gst-launch",
			"filesrc", "location=/dev/stdin",
			"!", "audio/x-raw-int,",
			"endianness=(int)1234,",
			"signed=(boolean)true,",
			"width=(int)16,",
			"depth=(int)16,",
			"rate=(int)"+rate+",",
			"channels=(int)2",
			"!", "queue", "!", "mixersink",
		)
	}
	return exec.Command("aplay", "-q", "-t", "raw", "-f", "S16_LE", "-c", "2", "-r", rate)
}

func openMP3(url string) (io.ReadCloser, error) {
	if isNetworkURL(url) {
		return newReconnectingHTTPStream(url)
	}
	return os.Open(url)
}

func requestMP3Stream(url string, offset int64) (*http.Response, error) {
	header := httpme.Header{"Accept-Encoding": "identity"}
	if offset > 0 {
		header["Range"] = "bytes=" + strconv.FormatInt(offset, 10) + "-"
	}
	resp, err := httpme.GetStream(url, header)
	if err != nil {
		return nil, err
	}
	if resp.R.StatusCode < http.StatusOK || resp.R.StatusCode >= http.StatusMultipleChoices {
		resp.R.Body.Close()
		return nil, fmt.Errorf("download status: %s", resp.R.Status)
	}
	return resp.R, nil
}

func responseTotalLength(resp *http.Response) int64 {
	if resp.StatusCode != http.StatusPartialContent {
		return resp.ContentLength
	}
	contentRange := resp.Header.Get("Content-Range")
	slash := strings.LastIndexByte(contentRange, '/')
	if slash < 0 || slash == len(contentRange)-1 {
		return -1
	}
	total, err := strconv.ParseInt(contentRange[slash+1:], 10, 64)
	if err != nil {
		return -1
	}
	return total
}

func isNetworkURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

func setPCMStream(url string, stream io.ReadCloser) bool {
	pcmStreamMu.Lock()
	defer pcmStreamMu.Unlock()
	if (IsStop && !IsPlayWeather) || NowUrl != url {
		return false
	}
	if pcmStream != nil {
		_ = pcmStream.Close()
	}
	pcmStream = stream
	return true
}

func clearPCMStream(stream io.ReadCloser) {
	pcmStreamMu.Lock()
	defer pcmStreamMu.Unlock()
	if pcmStream == stream {
		pcmStream = nil
	}
}

func cancelPlatformPlayback() {
	pcmStreamMu.Lock()
	defer pcmStreamMu.Unlock()
	if pcmStream != nil {
		_ = pcmStream.Close()
		pcmStream = nil
	}
}

func pausePlatformPlayback() error {
	return signalUnixCmd(syscall.SIGSTOP)
}

func resumePlatformPlayback() error {
	return signalUnixCmd(syscall.SIGCONT)
}

func setPlatformVolume(value string) error {
	if isKindle() {
		return nil
	}
	return runAmixer(strings.TrimSuffix(value, "%") + "%")
}

func changePlatformVolume(value string) error {
	if isKindle() {
		return nil
	}
	return runAmixer(value)
}

func runAmixer(value string) error {
	var lastErr error
	for _, control := range []string{"Master", "PCM", "Speaker"} {
		if err := exec.Command("amixer", "-q", "sset", control, value).Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("amixer Master/PCM/Speaker: %w", lastErr)
}
