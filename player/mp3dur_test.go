package player

import (
	"bufio"
	"os"
	"testing"

	"github.com/hajimehoshi/go-mp3"
)

// 用仓库里的 play.mp3 对一遍：probe 解析出来的时长要和解码器扫全帧算出来的接近。
// 解码器只有本地文件（可 Seek）才算得出 Length()，正好当参照。
func TestProbeMP3Duration(t *testing.T) {
	const path = "../play.mp3"
	f, err := os.Open(path)
	if err != nil {
		t.Skip("没有 play.mp3，跳过")
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReaderSize(f, 64*1024)
	got := probeStream(br, st.Size())
	if got <= 0 {
		t.Fatalf("probe 返回 %d，应该能解析出来", got)
	}

	// 参照值要用另一个句柄：解码器必须先看到 *os.File 这个 Seeker 才肯扫全帧
	ref, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()

	dec, err := mp3.NewDecoder(ref)
	if err != nil {
		t.Fatal(err)
	}
	length := dec.Length()
	rate := dec.SampleRate()
	if length <= 0 || rate <= 0 {
		t.Skip("解码器给不出 Length()")
	}
	want := length * 1000 / 4 / int64(rate)

	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	// 允许 2% 误差（CBR 估算 / 尾部标签都会带来偏差）
	if diff*100 > want*2 {
		t.Fatalf("时长差太多：probe=%dms 解码器=%dms", got, want)
	}
	t.Logf("probe=%dms 解码器=%dms 误差=%dms (%.2f%%)", got, want, diff, float64(diff)*100/float64(want))
}
