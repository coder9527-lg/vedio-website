// 用户与角色管理（仅 admin，user.manage 权限）。
// 页面为 SSR，增删改走 /api/v1/admin/users 的 JSON 接口；
// 所有写操作都依赖持久化，未配置 DB_PATH 时中间件直接 503，不会进到这里。
package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// userView 是面向模板/接口的账号视图，不泄露密码哈希。
type userView struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"displayName"`
	Role         string    `json:"role"`
	RoleLabel    string    `json:"roleLabel"`
	TokenVersion int       `json:"tokenVersion"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// roleOptions 供页面渲染角色下拉，固定顺序与中文标签。
type roleOption struct {
	Value string
	Label string
}

var userRoleOptions = []roleOption{
	{store.RoleAdmin, "管理员（全部权限）"},
	{store.RolePM, "内容管理员（PM）"},
	{store.RoleViewer, "观看者"},
}

func roleLabel(role string) string {
	for _, o := range userRoleOptions {
		if o.Value == role {
			return o.Label
		}
	}
	return role
}

func toUserView(u *store.User) userView {
	return userView{
		ID:           u.ID,
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		Role:         u.Role,
		RoleLabel:    roleLabel(u.Role),
		TokenVersion: u.TokenVersion,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}

type userAdminPage struct {
	basePage
	Users       []userView
	Roles       []roleOption
	CurrentUser *CurrentUser
}

// decodeUserBody 限制请求体大小并解析 JSON。
func decodeUserBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(v)
}

// handleUserAdminPage 用户管理 SSR 页面。
func (s *Server) handleUserAdminPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	users, err := s.store.ListUsers()
	if err != nil {
		log.Printf("查询用户列表失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询用户失败"})
		return
	}
	views := make([]userView, 0, len(users))
	for _, u := range users {
		views = append(views, toUserView(u))
	}
	s.renderWithUser(w, r, "admin_users.html", &userAdminPage{
		basePage:    basePage{Title: "用户与角色", AuthEnabled: s.authEnabled()},
		Users:       views,
		Roles:       userRoleOptions,
		CurrentUser: userFromContext(r),
	})
}

type createUserRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}

// handleUserCreate 新建账号。重名 409、非法角色 400。
func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req createUserRequest
	if err := decodeUserBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	if !auth.ValidRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法角色"})
		return
	}
	u, err := s.store.CreateUser(req.Username, req.DisplayName, req.Password, req.Role)
	if err != nil {
		if errors.Is(err, store.ErrDuplicateUser) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "用户名已存在"})
			return
		}
		log.Printf("创建用户失败: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 首个账号会把系统从单密码模式切换到多用户模式
	s.refreshMultiUser()
	writeJSON(w, http.StatusCreated, toUserView(u))
}

type updateRoleRequest struct {
	Role string `json:"role"`
}

// handleUserUpdateRole 修改角色，最后一个 admin 禁止降级（409）。
func (s *Server) handleUserUpdateRole(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	var req updateRoleRequest
	if err := decodeUserBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	if !auth.ValidRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法角色"})
		return
	}
	if err := s.store.UpdateRole(id, req.Role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "用户不存在"})
			return
		}
		// 最后一个管理员降级保护等业务约束
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	u, err := s.store.GetUserByID(id)
	if err != nil || u == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取更新后的用户失败"})
		return
	}
	writeJSON(w, http.StatusOK, toUserView(u))
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

// handleUserResetPassword 重置密码并使该账号旧 Cookie 即时失效。
func (s *Server) handleUserResetPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	var req resetPasswordRequest
	if err := decodeUserBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	if req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "密码不能为空"})
		return
	}
	if err := s.store.UpdatePassword(id, req.Password); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "用户不存在"})
			return
		}
		log.Printf("重置密码失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "重置密码失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUserDelete 删除账号，最后一个 admin 禁止删除（409）。
func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteUser(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "用户不存在"})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// parseUserID 解析路径中的用户 ID，非法或缺失时已写出 400/404 并返回 false。
func parseUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法用户 ID"})
		return 0, false
	}
	return id, true
}
