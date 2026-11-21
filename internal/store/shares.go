package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Share 是一条匿名下载分享记录。提取码仅保存 SHA-256 哈希，PasscodeHash 为空串表示无码直链。
type Share struct {
	Token        string    `json:"token"`
	VideoID      string    `json:"videoId"`
	PasscodeHash string    `json:"-"` // 哈希不外泄，JSON 响应只回 HasPasscode
	ExpiresAt    time.Time `json:"expiresAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

// HasPasscode 表示该分享是否设置了提取码。
func (s *Share) HasPasscode() bool { return s.PasscodeHash != "" }

// newShareToken 用 crypto/rand 生成 32 位 hex token（128bit 熵，枚举不可行）。
func newShareToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashPasscode 返回提取码的 SHA-256 hex 哈希；空码返回空串（无码分享）。
func hashPasscode(passcode string) string {
	if passcode == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(passcode))
	return hex.EncodeToString(sum[:])
}

// CreateShare 为指定视频创建分享。hours 为有效时长（小时），passcode 为空表示无码链接。
// token 碰撞时最多重试 3 次。
func (s *Store) CreateShare(videoID string, hours int, passcode string) (*Share, error) {
	if videoID == "" {
		return nil, fmt.Errorf("videoID 不能为空")
	}
	if hours <= 0 {
		return nil, fmt.Errorf("有效期必须为正数")
	}
	now := time.Now().UTC()
	var token string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		token, err = newShareToken()
		if err != nil {
			return nil, err
		}
		var exists int
		if err = s.db.QueryRow(`SELECT COUNT(1) FROM shares WHERE token = ?`, token).Scan(&exists); err != nil {
			return nil, err
		}
		if exists > 0 {
			continue // 极小概率碰撞，换一个 token 重试
		}
		sh := &Share{
			Token:        token,
			VideoID:      videoID,
			PasscodeHash: hashPasscode(passcode),
			ExpiresAt:    now.Add(time.Duration(hours) * time.Hour),
			CreatedAt:    now,
		}
		if _, err = s.db.Exec(
			`INSERT INTO shares(token, video_id, passcode_hash, expires_at, created_at)
			 VALUES(?, ?, ?, ?, ?)`,
			sh.Token, sh.VideoID, sh.PasscodeHash, sh.ExpiresAt, sh.CreatedAt); err != nil {
			return nil, err
		}
		return sh, nil
	}
	return nil, fmt.Errorf("生成分享 token 连续碰撞，请重试")
}

// GetShare 按 token 查询分享；无记录返回 (nil, nil)。
func (s *Store) GetShare(token string) (*Share, error) {
	sh := &Share{}
	err := s.db.QueryRow(
		`SELECT token, video_id, passcode_hash, expires_at, created_at FROM shares WHERE token = ?`,
		token,
	).Scan(&sh.Token, &sh.VideoID, &sh.PasscodeHash, &sh.ExpiresAt, &sh.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sh, nil
}

// ListSharesByVideo 返回某视频的全部有效/无效分享（按创建时间倒序），供管理端列表展示。
func (s *Store) ListSharesByVideo(videoID string) ([]Share, error) {
	rows, err := s.db.Query(
		`SELECT token, video_id, passcode_hash, expires_at, created_at
		 FROM shares WHERE video_id = ? ORDER BY created_at DESC`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Share
	for rows.Next() {
		var sh Share
		if err := rows.Scan(&sh.Token, &sh.VideoID, &sh.PasscodeHash, &sh.ExpiresAt, &sh.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// DeleteShare 撤销分享；记录不存在返回 ErrNotFound。
func (s *Store) DeleteShare(token string) error {
	res, err := s.db.Exec(`DELETE FROM shares WHERE token = ?`, token)
	if err != nil {
		return err
	}
	return requireRowAffected(res)
}

// DeleteExpiredShares 删除全部已过期分享，返回删除条数。
func (s *Store) DeleteExpiredShares(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM shares WHERE expires_at <= ?`, now.UTC())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

// VerifyPasscode 校验明文提取码与存储哈希是否匹配；无码分享恒为 true。
func (s *Share) VerifyPasscode(passcode string) bool {
	if s.PasscodeHash == "" {
		return true
	}
	return s.PasscodeHash == hashPasscode(passcode)
}
