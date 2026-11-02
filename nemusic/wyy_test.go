package nemusic

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// AES 加密后能被标准库解密还原，且 base64 不带换行
func TestAESEncryptCBC(t *testing.T) {
	plain := `{"ids":"[7238116782]","csrf_token":"","level":"standard"}`
	enc, err := aesEncryptCBC([]byte(plain), wyyAESKey, wyyAESIV)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(enc, "\r\n") {
		t.Fatal("base64 不应该包含换行")
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("base64 解码失败: %v", err)
	}
	if len(raw)%aes.BlockSize != 0 {
		t.Fatalf("密文长度 %d 不是块大小整数倍", len(raw))
	}
	block, err := aes.NewCipher(to16Bytes(wyyAESKey))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, to16Bytes(wyyAESIV)).CryptBlocks(out, raw)
	pad := int(out[len(out)-1])
	if pad <= 0 || pad > aes.BlockSize {
		t.Fatalf("padding 长度不合法: %d", pad)
	}
	if string(out[:len(out)-pad]) != plain {
		t.Fatalf("解密结果不一致: %s", string(out[:len(out)-pad]))
	}
}

// 明文正好是块大小整数倍时，要补满一整块
func TestAESEncryptCBCFullBlockPad(t *testing.T) {
	plain := strings.Repeat("a", aes.BlockSize)
	enc, err := aesEncryptCBC([]byte(plain), wyyAESKey, wyyAESIV)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != aes.BlockSize*2 {
		t.Fatalf("补齐后长度应为 %d，实际 %d", aes.BlockSize*2, len(raw))
	}
}

// RSA 结果必须是 256 位 hex
func TestRSAEncrypt(t *testing.T) {
	out, err := rsaEncrypt(reverseString("abcdefghijklmnop"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 256 {
		t.Fatalf("encSecKey 长度应为 256，实际 %d", len(out))
	}
	if _, err := hex.DecodeString(out); err != nil {
		t.Fatalf("encSecKey 不是合法hex: %v", err)
	}
	// 同样的输入必须得到同样的结果
	out2, err := rsaEncrypt(reverseString("abcdefghijklmnop"))
	if err != nil {
		t.Fatal(err)
	}
	if out != out2 {
		t.Fatal("RSA 结果不稳定")
	}
}

// 签名产物长度必须符合接口要求
func TestWeapiSign(t *testing.T) {
	params, encSecKey, err := weapiSign(`{"ids":"[7238116782]","csrf_token":"","level":"standard"}`)
	if err != nil {
		t.Fatal(err)
	}
	if params == "" {
		t.Fatal("params 为空")
	}
	if strings.ContainsAny(params, "\r\n") {
		t.Fatal("params 不应该包含换行")
	}
	raw, err := base64.StdEncoding.DecodeString(params)
	if err != nil {
		t.Fatalf("params 不是合法base64: %v", err)
	}
	if len(raw)%aes.BlockSize != 0 {
		t.Fatalf("params 密文长度 %d 不是块大小整数倍", len(raw))
	}
	if len(encSecKey) != 256 {
		t.Fatalf("encSecKey 长度应为 256，实际 %d", len(encSecKey))
	}
}

func TestRandKey(t *testing.T) {
	for i := 0; i < 10; i++ {
		key, err := randKey(16)
		if err != nil {
			t.Fatal(err)
		}
		if len(key) != 16 {
			t.Fatalf("随机密钥长度应为 16，实际 %d", len(key))
		}
		for _, c := range key {
			if !strings.ContainsRune(wyyKeyChars, c) {
				t.Fatalf("随机密钥包含非法字符 %q", c)
			}
		}
	}
}

func TestCsrfFromCookie(t *testing.T) {
	cases := []struct {
		cookie string
		want   string
	}{
		{"", ""},
		{"__csrf=abc123", "abc123"},
		{"MUSIC_U=xxx; __csrf=abc123; NMTID=yyy", "abc123"},
		{" MUSIC_U=xxx ;__csrf=abc", "abc"},
		{"__csrf=; MUSIC_U=xxx", ""},
		{"MUSIC_U=xxx", ""},
	}
	for _, c := range cases {
		if got := csrfFromCookie(c.cookie); got != c.want {
			t.Fatalf("csrfFromCookie(%q) = %q, 期望 %q", c.cookie, got, c.want)
		}
	}
}

func TestReverseString(t *testing.T) {
	if got := reverseString("abc"); got != "cba" {
		t.Fatalf("reverseString(abc) = %q", got)
	}
	if got := reverseString(""); got != "" {
		t.Fatalf("reverseString(空) = %q", got)
	}
}
