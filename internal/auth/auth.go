// Package auth 提供无数据库的简单访问密码认证：
// 登录成功后下发 HMAC-SHA256 签名的无状态 Cookie，服务端不保存任何会话。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const CookieName = "vw_session"

var (
	ErrNoToken      = errors.New("缺少登录凭证")
	ErrInvalidToken = errors.New("登录凭证无效")
)

// Authenticator 负责密码校验、令牌签发/验证以及按来源 IP 的失败限流。
type Authenticator struct {
	password   []byte
	secret     []byte
	ttl        time.Duration
	cookieName string

	maxFails int
	window   time.Duration

	mu       sync.Mutex
	failures map[string]*failState
}

type failState struct {
	count    int
	firstAt  time.Time
	lockedAt time.Time
}

// Option 用于调整可选参数，主要服务于测试。
type Option func(*Authenticator)

// WithLimit 配置登录失败限流：window 窗口内失败 maxFails 次后锁定到窗口结束。
func WithLimit(maxFails int, window time.Duration) Option {
	return func(a *Authenticator) {
		a.maxFails = maxFails
		a.window = window
	}
}

// New 创建认证器。secret 为空时随机生成（进程重启后旧令牌全部失效）。
func New(password, secret string, ttl time.Duration, opts ...Option) (*Authenticator, error) {
	if password == "" {
		return nil, errors.New("auth: 密码不能为空")
	}
	key := []byte(secret)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("auth: 生成签名密钥失败: %w", err)
		}
	}
	a := &Authenticator{
		password:   []byte(password),
		secret:     key,
		ttl:        ttl,
		cookieName: CookieName,
		maxFails:   10,
		window:     10 * time.Minute,
		failures:   make(map[string]*failState),
	}
	for _, o := range opts {
		o(a)
	}
	return a, nil
}

// Enabled 始终为 true；保留方法以便中间件统一判断。
func (a *Authenticator) Enabled() bool { return true }

// sign 对 payload 计算 HMAC，返回 base64(rawurl) 形式。
func (a *Authenticator) sign(payload string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue 生成一个带过期时间的签名令牌：exp.payload.sig。
func (a *Authenticator) Issue(now time.Time) string {
	exp := now.Add(a.ttl).Unix()
	payload := fmt.Sprintf("%d", exp)
	return payload + "." + a.sign(payload)
}

// Verify 校验令牌签名与有效期。
func (a *Authenticator) Verify(token string, now time.Time) error {
	expB64, sig, ok := strings.Cut(token, ".")
	if !ok {
		return ErrInvalidToken
	}
	want := a.sign(expB64)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return ErrInvalidToken
	}
	var exp int64
	if _, err := fmt.Sscanf(expB64, "%d", &exp); err != nil {
		return ErrInvalidToken
	}
	if now.Unix() > exp {
		return ErrInvalidToken
	}
	return nil
}

// CheckRequest 从请求 Cookie 中提取并验证令牌。
func (a *Authenticator) CheckRequest(r *http.Request) error {
	c, err := r.Cookie(a.cookieName)
	if err != nil || c.Value == "" {
		return ErrNoToken
	}
	return a.Verify(c.Value, time.Now())
}

// SetCookie 把登录令牌写入 HttpOnly Cookie。
func (a *Authenticator) SetCookie(w http.ResponseWriter, r *http.Request, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName,
		Value:    a.Issue(time.Now()),
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
}

// ClearCookie 使浏览器删除登录 Cookie。
func (a *Authenticator) ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// CheckPassword 常量时间比较密码。
func (a *Authenticator) CheckPassword(input string) bool {
	return subtle.ConstantTimeCompare([]byte(input), a.password) == 1
}

// AllowLogin 判断某 IP 当前是否可尝试登录（限流）。
func (a *Authenticator) AllowLogin(ip string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.failures[ip]
	if !ok {
		return true
	}
	if now.Sub(st.firstAt) > a.window {
		delete(a.failures, ip)
		return true
	}
	// 窗口内达到上限即锁到窗口结束
	return st.count < a.maxFails
}

// RecordFailure 记录一次失败登录。
func (a *Authenticator) RecordFailure(ip string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.failures[ip]
	if !ok || now.Sub(st.firstAt) > a.window {
		a.failures[ip] = &failState{count: 1, firstAt: now}
		return
	}
	st.count++
}

// ResetFailures 登录成功后清空该 IP 的失败记录。
func (a *Authenticator) ResetFailures(ip string) {
	a.mu.Lock()
	delete(a.failures, ip)
	a.mu.Unlock()
}

// ClientIP 取 TCP 连接对端地址（忽略可伪造的 X-Forwarded-For）。
func ClientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}
