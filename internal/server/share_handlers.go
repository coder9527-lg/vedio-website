package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// shareCookieName 是提取码校验通过后下发的 HttpOnly Cookie 名。
const shareCookieName = "vw_share"

// allowedShareHours 是允许创建分享的有效期白名单（1/7/30 天），防止任意透传。
var allowedShareHours = map[int]bool{24: true, 168: true, 720: true}

// maxSharePasscodeLen 限制提取码长度。
const maxSharePasscodeLen = 64

// sharePageData 是匿名分享页的模板数据，只暴露单个视频的有限信息。
type sharePageData struct {
	basePage
	Token       string
	VideoName   string
	SizeText    string
	HasPasscode bool
	ExpiresAt   time.Time
	Error       string
}

// shareView 是管理端分享列表的 JSON 视图，不回传提取码哈希。
type shareView struct {
	Token       string    `json:"token"`
	VideoID     string    `json:"videoId"`
	HasPasscode bool      `json:"hasPasscode"`
	ExpiresAt   time.Time `json:"expiresAt"`
	CreatedAt   time.Time `json:"createdAt"`
}

// shareCookieValue 生成与具体分享绑定的 Cookie 值：token 与提取码哈希拼接后再做一次 SHA-256。
func shareCookieValue(token, passcodeHash string) string {
	sum := sha256.Sum256([]byte(token + ":" + passcodeHash))
	return hex.EncodeToString(sum[:])
}

// setShareCookie 下发校验通过 Cookie，有效期与分享过期时间对齐。
func setShareCookie(w http.ResponseWriter, sh *store.Share, secure bool) {
	maxAge := int(time.Until(sh.ExpiresAt).Seconds())
	if maxAge <= 0 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     shareCookieName,
		Value:    shareCookieValue(sh.Token, sh.PasscodeHash),
		Path:     "/share/" + sh.Token,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// cookieAuthorized 判断请求 Cookie 是否通过该分享的提取码校验。
func cookieAuthorized(r *http.Request, sh *store.Share) bool {
	c, err := r.Cookie(shareCookieName)
	if err != nil {
		return false
	}
	return c.Value == shareCookieValue(sh.Token, sh.PasscodeHash)
}

// lookupLiveShare 取出分享并做过期惰性清理：不存在返回 nil，过期则删行后返回 nil。
func (s *Server) lookupLiveShare(w http.ResponseWriter, token string) *store.Share {
	sh, err := s.store.GetShare(token)
	if err != nil {
		log.Printf("查询分享失败: %v", err)
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return nil
	}
	if sh == nil {
		http.NotFound(w, nil)
		return nil
	}
	if time.Now().UTC().After(sh.ExpiresAt) {
		if err := s.store.DeleteShare(token); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("惰性清理过期分享失败: %v", err)
		}
		http.Error(w, "该分享链接已过期", http.StatusGone)
		return nil
	}
	return sh
}

// renderSharePage 渲染匿名分享页；status 用于错码等场景回传非 200 状态。
func (s *Server) renderSharePage(w http.ResponseWriter, status int, sh *store.Share, v *media.Video, errMsg string) {
	w.WriteHeader(status)
	s.render(w, "share.html", sharePageData{
		basePage:    basePage{Title: "文件分享", AuthEnabled: s.authEnabled()},
		Token:       sh.Token,
		VideoName:   v.Name + v.Ext,
		SizeText:    humanSize(v.Size),
		HasPasscode: sh.HasPasscode(),
		ExpiresAt:   sh.ExpiresAt,
		Error:       errMsg,
	})
}

// handleSharePage 匿名分享落地页：展示视频名/大小，有提取码时渲染输入表单。
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	token := r.PathValue("token")
	sh := s.lookupLiveShare(w, token)
	if sh == nil {
		// lookupLiveShare 已写出 404/410/500
		return
	}
	v, ok := s.library.Get(sh.VideoID)
	if !ok {
		// 视频文件已从媒体库移除，分享失效
		_ = s.store.DeleteShare(token)
		http.NotFound(w, r)
		return
	}
	// 无码分享或已通过提取码校验，直接展示下载入口；否则展示提取码表单
	errMsg := ""
	if sh.HasPasscode() && !cookieAuthorized(r, sh) {
		errMsg = "" // 首次访问不报错，仅展示表单
	}
	s.renderSharePage(w, http.StatusOK, sh, v, errMsg)
}

// handleShareVerify 校验提取码：通过写 Cookie 后 302 回分享页，错码回 403 表单。
func (s *Server) handleShareVerify(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	token := r.PathValue("token")
	sh := s.lookupLiveShare(w, token)
	if sh == nil {
		return
	}
	v, ok := s.library.Get(sh.VideoID)
	if !ok {
		_ = s.store.DeleteShare(token)
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderSharePage(w, http.StatusBadRequest, sh, v, "表单解析失败")
		return
	}
	passcode := r.PostFormValue("passcode")
	if !sh.VerifyPasscode(passcode) {
		s.renderSharePage(w, http.StatusForbidden, sh, v, "提取码错误，请重试")
		return
	}
	setShareCookie(w, sh, s.cookieSecure)
	http.Redirect(w, r, "/share/"+token, http.StatusSeeOther)
}

// handleShareDownload 匿名下载：Cookie 或查询参数 passcode 二选一鉴权，强制 attachment。
func (s *Server) handleShareDownload(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	token := r.PathValue("token")
	sh := s.lookupLiveShare(w, token)
	if sh == nil {
		return
	}
	v, ok := s.library.Get(sh.VideoID)
	if !ok {
		_ = s.store.DeleteShare(token)
		http.NotFound(w, r)
		return
	}
	if sh.HasPasscode() && !cookieAuthorized(r, sh) {
		// Cookie 未命中时允许查询参数携带提取码（便于下载工具直接拉取）
		if !sh.VerifyPasscode(r.URL.Query().Get("passcode")) {
			http.Error(w, "提取码错误或缺失", http.StatusForbidden)
			return
		}
	}
	// 复用统一的 Range 文件服务，强制下载文件名
	serveVideoFile(w, r, v, true)
}

// ---------- 管理端（受鉴权链与 requireStore 保护）----------

// shareCreateRequest 是创建分享的请求体。
type shareCreateRequest struct {
	Hours    int    `json:"hours"`
	Passcode string `json:"passcode"`
}

// handleShareCreate 为指定视频创建匿名分享。
func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	var req shareCreateRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return
		}
	}
	if !allowedShareHours[req.Hours] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hours 只允许 24/168/720"})
		return
	}
	if len(req.Passcode) > maxSharePasscodeLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "提取码不能超过 64 个字符"})
		return
	}
	sh, err := s.store.CreateShare(id, req.Hours, req.Passcode)
	if err != nil {
		log.Printf("创建分享失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "创建分享失败"})
		return
	}
	writeJSON(w, http.StatusCreated, toShareView(sh))
}

// handleShareList 列出某视频的全部分享（含已过期未清理的记录，供管理端撤销）。
func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	shares, err := s.store.ListSharesByVideo(id)
	if err != nil {
		log.Printf("查询分享列表失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询分享失败"})
		return
	}
	views := make([]shareView, 0, len(shares))
	for i := range shares {
		views = append(views, toShareView(&shares[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": views})
}

// handleShareDelete 撤销一条分享。
func (s *Server) handleShareDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	token := r.PathValue("token")
	if err := s.store.DeleteShare(token); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "share not found"})
			return
		}
		log.Printf("撤销分享失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "撤销分享失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// toShareView 把存储模型转为管理端 JSON 视图。
func toShareView(sh *store.Share) shareView {
	return shareView{
		Token:       sh.Token,
		VideoID:     sh.VideoID,
		HasPasscode: sh.HasPasscode(),
		ExpiresAt:   sh.ExpiresAt,
		CreatedAt:   sh.CreatedAt,
	}
}
