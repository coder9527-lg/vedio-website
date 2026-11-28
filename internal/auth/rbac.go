// RBAC：角色、权限点与权限矩阵，以及携带用户身份的无状态签名令牌。
// 角色字符串与 internal/store 中的常量保持一致，权限矩阵在此单点维护、可审计。
package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 内置角色（与 store.Role* 同值）。
const (
	RoleAdmin  = "admin"
	RolePM     = "pm"
	RoleViewer = "viewer"
)

// 权限点常量。
const (
	PermLibraryRead   = "library.read"   // 浏览/搜索/详情/播放/下载/海报/字幕
	PermLibraryScan   = "library.scan"   // 触发媒体库重扫
	PermMetadataEdit  = "metadata.edit"  // 元信息编辑、TMDB 刮削
	PermContentOrg    = "content.organize" // 标签、合集、智能清单、书签、片头跳过
	PermPersonal      = "personal.rating" // 个人评分、稍后再看、进度、收藏、历史
	PermShareManage   = "share.manage"   // 匿名分享创建/吊销
	PermTranscodeAdmin = "transcode.admin" // 转码任务查看/终止
	PermSystemHealth  = "system.health"  // 数据/缓存健康、存储分析、转码管理页
	PermUserManage    = "user.manage"    // 用户与角色管理
)

// rolePerms 是角色 → 权限点集合的静态矩阵。
var rolePerms = map[string]map[string]bool{
	RoleAdmin: {
		PermLibraryRead: true, PermLibraryScan: true, PermMetadataEdit: true,
		PermContentOrg: true, PermPersonal: true, PermShareManage: true,
		PermTranscodeAdmin: true, PermSystemHealth: true, PermUserManage: true,
	},
	// 内容管理员（产品经理）：管片库内容与分享，不含系统/账号/转码任务/扫描控制。
	RolePM: {
		PermLibraryRead: true, PermMetadataEdit: true,
		PermContentOrg: true, PermPersonal: true, PermShareManage: true,
	},
	RoleViewer: {
		PermLibraryRead: true, PermContentOrg: true, PermPersonal: true,
	},
}

// RoleHasPerm 判定角色是否拥有权限点；未知角色一律拒绝。
func RoleHasPerm(role, perm string) bool {
	perms, ok := rolePerms[role]
	if !ok {
		return false
	}
	return perms[perm]
}

// ValidRole 判定角色是否为内置角色。
func ValidRole(role string) bool {
	_, ok := rolePerms[role]
	return ok
}

// Claims 是登录态中携带的用户身份。
type Claims struct {
	UserID       int64
	Role         string
	TokenVersion int
}

// 多用户令牌格式：exp.uid.role.ver.sig（旧单密码格式仍为 exp.sig，由 Verify 兼容）。

var errMalformedToken = errors.New("令牌格式错误")

// IssueUser 为多用户模式签发携带身份的令牌。
func (a *Authenticator) IssueUser(now time.Time, c Claims) string {
	exp := now.Add(a.ttl).Unix()
	payload := fmt.Sprintf("%d.%d.%s.%d", exp, c.UserID, c.Role, c.TokenVersion)
	return payload + "." + a.sign(payload)
}

// parseUserPayload 校验多用户令牌签名并解析身份，不负责 token_version 的 DB 比对。
func (a *Authenticator) parseUserToken(token string, now time.Time) (Claims, error) {
	// payload 自身含 4 段，加末尾签名共 5 段
	parts := strings.Split(token, ".")
	if len(parts) != 5 {
		return Claims{}, errMalformedToken
	}
	payload := strings.Join(parts[:4], ".")
	sig := parts[4]
	if subtleEqual(a.sign(payload), sig) != 1 {
		return Claims{}, ErrInvalidToken
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	if now.Unix() > exp {
		return Claims{}, ErrInvalidToken
	}
	uid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || uid <= 0 {
		return Claims{}, ErrInvalidToken
	}
	role := parts[2]
	if !ValidRole(role) {
		return Claims{}, ErrInvalidToken
	}
	ver, err := strconv.Atoi(parts[3])
	if err != nil || ver < 1 {
		return Claims{}, ErrInvalidToken
	}
	return Claims{UserID: uid, Role: role, TokenVersion: ver}, nil
}

// VerifyUser 校验多用户令牌并返回身份声明。
func (a *Authenticator) VerifyUser(token string, now time.Time) (Claims, error) {
	return a.parseUserToken(token, now)
}

// CheckUserRequest 从请求 Cookie 提取并校验多用户令牌。
func (a *Authenticator) CheckUserRequest(r *http.Request) (Claims, error) {
	c, err := r.Cookie(a.cookieName)
	if err != nil || c.Value == "" {
		return Claims{}, ErrNoToken
	}
	return a.VerifyUser(c.Value, time.Now())
}

// SetUserCookie 把携带身份的令牌写入 HttpOnly Cookie。
func (a *Authenticator) SetUserCookie(w http.ResponseWriter, r *http.Request, secure bool, c Claims) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName,
		Value:    a.IssueUser(time.Now(), c),
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
}

// subtleEqual 包一层常量时间比较，避免本文件再 import crypto/subtle 与既有 Verify 重复。
func subtleEqual(x, y string) int {
	if len(x) != len(y) {
		return 0
	}
	var v byte
	for i := 0; i < len(x); i++ {
		v |= x[i] ^ y[i]
	}
	if v == 0 {
		return 1
	}
	return 0
}
