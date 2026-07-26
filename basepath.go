package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// basePathAlphabet 避开容易看错的字符。
const basePathAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// LoadBasePath 读取或生成随机访问路径，形如 /aB3xY9pQ。
// 和 3x-ui 一样：路径本身也是一层门槛，扫端口的探不到界面。
func LoadBasePath(dir string) (string, bool, error) {
	path := filepath.Join(dir, "basepath")

	blob, err := os.ReadFile(path)
	if err == nil {
		if bp := strings.TrimSpace(string(blob)); bp != "" {
			return normalizeBasePath(bp), false, nil
		}
	} else if !os.IsNotExist(err) {
		return "", false, err
	}

	bp, err := randomBasePath(10)
	if err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(bp+"\n"), 0600); err != nil {
		return "", false, fmt.Errorf("写访问路径失败: %w", err)
	}
	return normalizeBasePath(bp), true, nil
}

func randomBasePath(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = basePathAlphabet[int(v)%len(basePathAlphabet)]
	}
	return string(out), nil
}

// normalizeBasePath 统一成 /xxx 的形式（无结尾斜杠）。
func normalizeBasePath(bp string) string {
	bp = strings.Trim(bp, "/")
	if bp == "" {
		return ""
	}
	return "/" + bp
}

// StripBasePath 把请求剥掉前缀后交给内层 handler。
// 前缀不匹配的请求一律 404，不泄漏这里跑着什么服务。
func StripBasePath(base string, next http.Handler) http.Handler {
	if base == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == base:
			// 少了结尾斜杠时补上，否则页面里的相对路径会拼错
			http.Redirect(w, r, base+"/", http.StatusTemporaryRedirect)
		case strings.HasPrefix(r.URL.Path, base+"/"):
			r.URL.Path = strings.TrimPrefix(r.URL.Path, base)
			next.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
