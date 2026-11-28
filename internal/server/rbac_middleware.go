// RBAC 接入层：把请求 Cookie 解析为 CurrentUser 并注入 context，
// 在 requireAuth 认证闸口之上提供 requirePerm 权限闸口。
// 双模式兼容：
//   - 单密码模式（默认）：旧 2 段令牌通过即视同内置 admin，拥有全部权限；
//   - 多用户模式（users 表至少一行）：解析 5 段令牌并回库比对 token_version，按角色鉴权；
//   - 未开启访问密码：维持零配置全站可用的既有基线，权限闸口直接放行。
package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/auth"
)

// CurrentUser 是注入请求上下文的登录身份。
type CurrentUser struct {
	ID          int64
	Username    string
	DisplayName string
	Role        string // admin / pm / viewer；匿名访客为空串
	LoggedIn    bool   // 是否已认证（区分全站公开时的匿名访客）
	MultiUser   bool   // 是否来自多用户模式（false 表示单密码内置管理员或匿名）
	AuthOn      bool   // 是否开启了访问密码（false=零配置全站开放，模板入口照常展示）
}

// 模板侧权限判断方法：basePage.CurrentUser.Can "system.health" 这类调用。
// 未开启访问密码时没有身份体系（LoggedIn=false 但属全站开放基线），模板入口照常展示，
// 与服务端 Server.can 的放行口径保持一致。
func (u *CurrentUser) Can(perm string) bool {
	if u == nil {
		return false
	}
	if !u.LoggedIn {
		return !u.AuthOn
	}
	return auth.RoleHasPerm(u.Role, perm)
}

// IsAdmin 供模板判断是否展示仅管理员入口（用户管理等）。
func (u *CurrentUser) IsAdmin() bool { return u != nil && u.LoggedIn && u.Role == auth.RoleAdmin }

type userCtxKey struct{}

// builtinAdmin 是单密码模式登录者持有的内置管理员身份。
func builtinAdmin() *CurrentUser {
	return &CurrentUser{
		Username:    "admin",
		DisplayName: "管理员",
		Role:        auth.RoleAdmin,
		LoggedIn:    true,
		AuthOn:      true,
	}
}

func anonymousUser(authOn bool) *CurrentUser { return &CurrentUser{AuthOn: authOn} }

// multiUserEnabled 判定是否进入多用户模式：已启用持久化且 users 表至少一个账号。
func (s *Server) multiUserEnabled() bool { return s.multiUser.Load() }

// refreshMultiUser 在启动与账号创建后重新评估模式。
// 最后一个管理员受保护、账号不会归零，因此开关只会从关到开，单调置真即可。
func (s *Server) refreshMultiUser() {
	if s.store == nil {
		return
	}
	if n, err := s.store.CountUsers(); err == nil && n > 0 {
		s.multiUser.Store(true)
	}
}

// currentUser 解析请求携带的登录凭证，返回当前身份（永不返回 nil）。
func (s *Server) currentUser(r *http.Request) *CurrentUser {
	// 未开启访问密码：匿名访客（权限闸口对此模式整体放行）
	if !s.authEnabled() {
		return anonymousUser(false)
	}
	if s.multiUserEnabled() {
		claims, err := s.authenticator.CheckUserRequest(r)
		if err != nil {
			return anonymousUser(true)
		}
		u, err := s.store.GetUserByID(claims.UserID)
		if err != nil || u == nil {
			return anonymousUser(true)
		}
		// 回库比对：改密/改角色会递增 token_version 并变更 role，使旧 Cookie 即时失效
		if u.TokenVersion != claims.TokenVersion || u.Role != claims.Role {
			return anonymousUser(true)
		}
		return &CurrentUser{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Role:        u.Role,
			LoggedIn:    true,
			MultiUser:   true,
			AuthOn:      true,
		}
	}
	// 单密码模式：旧格式令牌校验通过即内置管理员
	if err := s.authenticator.CheckRequest(r); err == nil {
		return builtinAdmin()
	}
	return anonymousUser(true)
}

// userFromContext 取出中间件注入的身份；缺失时按匿名访客处理（默认认证开启）。
func userFromContext(r *http.Request) *CurrentUser {
	if u, ok := r.Context().Value(userCtxKey{}).(*CurrentUser); ok && u != nil {
		return u
	}
	return anonymousUser(true)
}

// can 判定当前请求是否持有指定权限点。
func (s *Server) can(r *http.Request, perm string) bool {
	// 未开启访问密码时没有身份体系，维持零配置全站开放的既有行为
	if !s.authEnabled() {
		return true
	}
	u := userFromContext(r)
	if !u.LoggedIn {
		return false
	}
	return auth.RoleHasPerm(u.Role, perm)
}

// requireAuth 认证闸口：公开路径与未开启认证时放行（注入匿名身份）；
// 未登录的 API 请求返回 401 JSON，页面请求 302 跳转登录页并带回跳地址。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if !s.authEnabled() || isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u)))
			return
		}
		if !u.LoggedIn {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			loc := "/login"
			if r.URL.Path != "/" && r.URL.Path != "" {
				loc = "/login?next=" + urlEncode(r.URL.RequestURI())
			}
			http.Redirect(w, r, loc, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u)))
	})
}

// requirePerm 权限闸口，包装单个处理器；依赖外层 requireAuth 已注入身份。
// 已登录但权限不足一律 403（API 返回 JSON，页面渲染无权访问页）。
func (s *Server) requirePerm(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.can(r, perm) {
			next(w, r)
			return
		}
		s.forbidden(w, r)
	}
}

// forbidden 统一输出 403：API 给 JSON，页面渲染专用无权访问模板。
func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: 权限不足"})
		return
	}
	w.WriteHeader(http.StatusForbidden)
	s.renderWithUser(w, r, "forbidden.html", &basePage{Title: "无权访问", AuthEnabled: s.authEnabled()})
}
