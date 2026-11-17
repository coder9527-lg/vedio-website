package server

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
)

type loginData struct {
	Title string
	Error string
	Next  string
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
	// 已登录则无需再看登录页
	if err := s.authenticator.CheckRequest(r); err == nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", loginData{Title: "登录", Next: r.URL.Query().Get("next")})
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
	if !s.authenticator.CheckPassword(password) {
		s.authenticator.RecordFailure(ip, now)
		log.Printf("登录失败: ip=%s", ip)
		s.renderLoginError(w, r, "密码错误")
		return
	}

	s.authenticator.ResetFailures(ip)
	s.authenticator.SetCookie(w, r, s.cookieSecure)
	log.Printf("登录成功: ip=%s", ip)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.authEnabled() {
		s.authenticator.ClearCookie(w, s.cookieSecure)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderLoginError(w http.ResponseWriter, r *http.Request, msg string) {
	w.WriteHeader(http.StatusUnauthorized)
	s.render(w, "login.html", loginData{
		Title: "登录",
		Error: msg,
		Next:  r.PostFormValue("next"),
	})
}
