package server

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
)

type loginData struct {
	Title     string
	Error     string
	Next      string
	MultiUser bool // 多用户模式下展示「用户名 + 密码」表单，否则仅访问密码
	Username  string // 登录失败时回填用户名，避免重输
}

// safeNext 只允许站内相对路径，防止开放重定向。
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// 已登录则无需再看登录页（同时兼容单密码旧令牌与多用户身份令牌）
	if s.currentUser(r).LoggedIn {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", loginData{
		Title:     "登录",
		Next:      r.URL.Query().Get("next"),
		MultiUser: s.multiUserEnabled(),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderLoginError(w, r, "表单解析失败")
		return
	}
	ip := auth.ClientIP(r)
	now := time.Now()
	if !s.authenticator.AllowLogin(ip, now) {
		log.Printf("登录被限流: ip=%s", ip)
		s.renderLoginError(w, r, "失败次数过多，请稍后再试")
		return
	}

	password := r.PostFormValue("password")
	next := safeNext(r.PostFormValue("next"))

	// 多用户模式：按用户名查账号并校验 bcrypt 密码，下发携带身份的 5 段令牌
	if s.multiUserEnabled() {
		username := strings.TrimSpace(r.PostFormValue("username"))
		u, err := s.store.GetUserByUsername(username)
		if err != nil {
			log.Printf("查询登录账号失败: ip=%s err=%v", ip, err)
			s.renderLoginError(w, r, "登录服务异常，请稍后再试")
			return
		}
		// 用户名不存在与密码错误返回同样提示，避免账号枚举
		if u == nil || !u.CheckPassword(password) {
			s.authenticator.RecordFailure(ip, now)
			log.Printf("多用户登录失败: ip=%s user=%q", ip, username)
			s.renderLoginError(w, r, "用户名或密码错误")
			return
		}
		s.authenticator.ResetFailures(ip)
		s.authenticator.SetUserCookie(w, r, s.cookieSecure, auth.Claims{
			UserID: u.ID, Role: u.Role, TokenVersion: u.TokenVersion,
		})
		log.Printf("登录成功: ip=%s user=%s role=%s", ip, u.Username, u.Role)
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}

	// 单密码模式（默认）：校验访问密码，通过者视同内置管理员
	if !s.authenticator.CheckPassword(password) {
		s.authenticator.RecordFailure(ip, now)
		log.Printf("登录失败: ip=%s", ip)
		s.renderLoginError(w, r, "密码错误")
		return
	}
	s.authenticator.ResetFailures(ip)
	s.authenticator.SetCookie(w, r, s.cookieSecure)
	log.Printf("登录成功: ip=%s (单密码内置管理员)", ip)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.authEnabled() {
		// 单密码与多用户令牌共用同一 Cookie 名，统一清除即可
		s.authenticator.ClearCookie(w, s.cookieSecure)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderLoginError(w http.ResponseWriter, r *http.Request, msg string) {
	w.WriteHeader(http.StatusUnauthorized)
	s.render(w, "login.html", loginData{
		Title:     "登录",
		Error:     msg,
		Next:      r.PostFormValue("next"),
		MultiUser: s.multiUserEnabled(),
		Username:  r.PostFormValue("username"),
	})
}
