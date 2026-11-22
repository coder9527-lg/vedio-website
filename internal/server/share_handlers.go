package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// shareCookieName 是提取码校验通过后下发的 HttpOnly Cookie 名。
const shareCookieName = "vw_share"

// allowedShareHours 是允许创建分享的有效期白名单（1/7/30 天），防止任意透传。
var allowedShareHours = map[int]bool{24: true, 168: true, 720: true}

// maxSharePasscodeLen 限制提取码长度。
const maxSharePasscodeLen = 64

// minSharePasscodeLen 是提取码的最小长度：过短的提取码（如 1 位）在线爆破成本过低。
const minSharePasscodeLen = 4

// shareLimitKey 把来源 IP 与分享 token 组合成限流键：同一 IP 对不同分享分别计数，
// 避免一条分享被爆破时波及该 IP 访问的其他分享。
func shareLimitKey(r *http.Request, token string) string {
	return auth.ClientIP(r) + "|" + token
}

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
	limitKey := shareLimitKey(r, token)
	if !s.shareLimiter.Allow(limitKey, time.Now()) {
		s.renderSharePage(w, http.StatusTooManyRequests, sh, v, "尝试次数过多，请 10 分钟后再试")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderSharePage(w, http.StatusBadRequest, sh, v, "表单解析失败")
		return
	}
	passcode := r.PostFormValue("passcode")
	if !sh.VerifyPasscode(passcode) {
		s.shareLimiter.RecordFailure(limitKey, time.Now())
		s.renderSharePage(w, http.StatusForbidden, sh, v, "提取码错误，请重试")
		return
	}
	s.shareLimiter.Reset(limitKey)
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
		limitKey := shareLimitKey(r, token)
		if !s.shareLimiter.Allow(limitKey, time.Now()) {
			http.Error(w, "尝试次数过多，请稍后再试", http.StatusTooManyRequests)
			return
		}
		// Cookie 未命中时允许查询参数携带提取码（便于下载工具直接拉取）
		if !sh.VerifyPasscode(r.URL.Query().Get("passcode")) {
			s.shareLimiter.RecordFailure(limitKey, time.Now())
			http.Error(w, "提取码错误或缺失", http.StatusForbidden)
			return
		}
		s.shareLimiter.Reset(limitKey)
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
	// 空串表示无码直链；一旦设置提取码，必须达到最小长度以防在线爆破。
	if req.Passcode != "" && len(req.Passcode) < minSharePasscodeLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "提取码至少 4 个字符（不需要提取码请留空）"})
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

// ---------- 分享管理中心（HTML 页面，受鉴权链保护）----------

// shareListItem 是管理中心列表中的一条分享，附带渲染所需的视频名与状态。
type shareListItem struct {
	Share      store.Share
	VideoName  string
	VideoOK    bool
	ShareURL   string
	Expired    bool
	ExpireText string
}

// shareListPage 是分享管理中心的页面视图模型。
type shareListPage struct {
	basePage
	StoreOn bool
	Items   []shareListItem
	Cleared int // 清理过期动作带回的成功条数，零值不显示提示
}

// handleShareCenter 渲染分享管理中心：全量列出分享，过期状态按当前时间实时判定。
func (s *Server) handleShareCenter(w http.ResponseWriter, r *http.Request) {
	data := shareListPage{
		basePage: basePage{Title: "分享管理", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
	}
	if !s.storeEnabled() {
		s.render(w, "shares.html", data)
		return
	}
	// ?cleared=N 由清理动作 PRG 带回，非法或缺省一律按 0 处理（不显示提示）
	if n, err := strconv.Atoi(r.URL.Query().Get("cleared")); err == nil && n > 0 {
		data.Cleared = n
	}

	shares, err := s.store.ListAllShares()
	if err != nil {
		log.Printf("查询全部分享失败: %v", err)
		http.Error(w, "查询分享列表失败", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	data.Items = make([]shareListItem, 0, len(shares))
	for _, sh := range shares {
		item := shareListItem{
			Share:    sh,
			ShareURL: "/share/" + sh.Token,
			Expired:  sh.ExpiresAt.Before(now),
		}
		item.ExpireText = sh.ExpiresAt.Local().Format("2006-01-02 15:04")
		if v, ok := s.library.Get(sh.VideoID); ok {
			item.VideoOK = true
			item.VideoName = v.Name + v.Ext
		} else {
			// 视频已从媒体库移除：保留分享记录但禁止跳转，提示用户可直接撤销
			item.VideoName = "已删除视频 #" + sh.VideoID
		}
		data.Items = append(data.Items, item)
	}

	s.render(w, "shares.html", data)
}

// handleShareRevoke 从管理中心撤销一条分享（按 token），PRG 跳回列表。
func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	token := r.PostFormValue("token")
	if token == "" {
		http.Error(w, "缺少分享 token", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteShare(token); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "分享不存在或已被撤销", http.StatusNotFound)
			return
		}
		log.Printf("撤销分享失败: %v", err)
		http.Error(w, "撤销分享失败", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/shares", http.StatusSeeOther)
}

// handleShareCleanExpired 一键清理全部已过期分享，按影响行数 PRG 回列表并提示。
func (s *Server) handleShareCleanExpired(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	n, err := s.store.DeleteExpiredShares(time.Now().UTC())
	if err != nil {
		log.Printf("清理过期分享失败: %v", err)
		http.Error(w, "清理过期分享失败", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/shares?cleared="+strconv.FormatInt(n, 10), http.StatusSeeOther)
}
