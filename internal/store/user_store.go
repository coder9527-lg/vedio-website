// 用户与角色仓储：RBAC 多用户模式的数据访问层。
// 密码一律 bcrypt 哈希存储，不保留明文；角色取值由应用层白名单约束。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// 内置角色常量，权限矩阵在 auth 包按这些值判定。
const (
	RoleAdmin  = "admin"  // 管理员：全部权限
	RolePM     = "pm"     // 内容管理员（产品经理）：片库内容与分享，不含系统/账号管理
	RoleViewer = "viewer" // 观看者：浏览播放与个人数据
)

// ErrDuplicateUser 用户名已存在。
var ErrDuplicateUser = errors.New("用户名已存在")

// User 是一个登录账号。
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"displayName"`
	Role         string `json:"role"`
	TokenVersion int    `json:"tokenVersion"`
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// PasswordHash 仅供仓储内部读写，不对外序列化。
	PasswordHash string `json:"-"`
}

const userColumns = `id, username, display_name, password_hash, role, token_version, created_at, updated_at`

// ValidRole 判定角色是否在白名单内。
func ValidRole(role string) bool {
	return role == RoleAdmin || role == RolePM || role == RoleViewer
}

// hashPassword 生成 bcrypt 哈希。
func hashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("哈希密码失败: %w", err)
	}
	return string(b), nil
}

// CheckPassword 校验明文密码与哈希是否匹配。
func (u *User) CheckPassword(plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(plain)) == nil
}

func scanUser(scanner interface{ Scan(...any) error }) (*User, error) {
	var u User
	if err := scanner.Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash,
		&u.Role, &u.TokenVersion, &u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser 创建账号（用户名唯一、角色白名单）。返回带 ID 的 User。
func (s *Store) CreateUser(username, displayName, password, role string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, errors.New("用户名和密码不能为空")
	}
	if !ValidRole(role) {
		return nil, fmt.Errorf("非法角色: %q", role)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO users (username, display_name, password_hash, role, token_version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`,
		username, strings.TrimSpace(displayName), hash, role, now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrDuplicateUser
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

// GetUserByID 按主键查询，不存在返回 (nil, nil)。
func (s *Store) GetUserByID(id int64) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// GetUserByUsername 按用户名查询（登录用），不存在返回 (nil, nil)。
func (s *Store) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, strings.TrimSpace(username))
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// ListUsers 列出全部账号（按 ID 升序），不含业务过滤。
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers 返回账号总数，用于判定是否进入多用户模式与最后一个 admin 保护。
func (s *Store) CountUsers() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountAdmins 返回管理员数量。
func (s *Store) CountAdmins() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE role = ?`, RoleAdmin).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// UpdatePassword 重置密码并递增 token_version（使旧 Cookie 失效）。
func (s *Store) UpdatePassword(id int64, newPassword string) error {
	if newPassword == "" {
		return errors.New("密码不能为空")
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE users SET password_hash = ?, token_version = token_version + 1, updated_at = ? WHERE id = ?`,
		hash, time.Now().UTC(), id,
	)
	return err
}

// UpdateRole 修改角色并递增 token_version；禁止把最后一个 admin 降级（由 handler 预判，这里再兜底）。
func (s *Store) UpdateRole(id int64, role string) error {
	if !ValidRole(role) {
		return fmt.Errorf("非法角色: %q", role)
	}
	u, err := s.GetUserByID(id)
	if err != nil {
		return err
	}
	if u == nil {
		return sql.ErrNoRows
	}
	if u.Role == RoleAdmin && role != RoleAdmin {
		admins, err := s.CountAdmins()
		if err != nil {
			return err
		}
		if admins <= 1 {
			return errors.New("不能降级最后一个管理员")
		}
	}
	_, err = s.db.Exec(
		`UPDATE users SET role = ?, token_version = token_version + 1, updated_at = ? WHERE id = ?`,
		role, time.Now().UTC(), id,
	)
	return err
}

// DeleteUser 删除账号；禁止删除最后一个 admin。
func (s *Store) DeleteUser(id int64) error {
	u, err := s.GetUserByID(id)
	if err != nil {
		return err
	}
	if u == nil {
		return sql.ErrNoRows
	}
	if u.Role == RoleAdmin {
		admins, err := s.CountAdmins()
		if err != nil {
			return err
		}
		if admins <= 1 {
			return errors.New("不能删除最后一个管理员")
		}
	}
	_, err = s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}
