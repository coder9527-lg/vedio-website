package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// orphanTables 是数据健康检查覆盖的七张表白名单：逻辑表名（对外稳定标识）→ 物理表名。
// 所有动态 SQL 的表名只允许取自这里，杜绝用户输入拼接进 SQL 的注入路径。
// 键顺序即体检页分区的固定展示顺序。
var orphanTables = []struct {
	logical  string
	physical string
}{
	{"progress", "progress"},
	{"favorites", "favorites"},
	{"playlist_item", "playlist_item"},
	{"bookmarks", "bookmarks"},
	{"skip_settings", "skip_settings"},
	{"watch_later", "watch_later"},
	{"watch_events", "watch_events"},
	{"video_tag", "video_tag"},
}

// orphanPhysicalTable 把逻辑表名解析为物理表名，未在白名单内返回错误。
func orphanPhysicalTable(table string) (string, error) {
	for _, t := range orphanTables {
		if t.logical == table {
			return t.physical, nil
		}
	}
	return "", fmt.Errorf("不支持的数据表: %s", table)
}

// OrphanReport 是一次数据健康体检的结果，按表给出指向「库中已不存在视频」的孤儿记录数。
type OrphanReport struct {
	Progress     int64
	Favorites    int64
	PlaylistItem int64
	Bookmarks    int64
	SkipSettings int64
	WatchLater   int64
	WatchEvents  int64
	VideoTag     int64
}

// orphanField 把逻辑表名映射到报告中的对应字段指针，便于按固定顺序遍历读写。
func (r *OrphanReport) orphanField(table string) *int64 {
	switch table {
	case "progress":
		return &r.Progress
	case "favorites":
		return &r.Favorites
	case "playlist_item":
		return &r.PlaylistItem
	case "bookmarks":
		return &r.Bookmarks
	case "skip_settings":
		return &r.SkipSettings
	case "watch_later":
		return &r.WatchLater
	case "watch_events":
		return &r.WatchEvents
	case "video_tag":
		return &r.VideoTag
	default:
		return nil
	}
}

// Total 返回八类孤儿记录总数，供页面顶部汇总与一键清理结果使用。
func (r OrphanReport) Total() int64 {
	return r.Progress + r.Favorites + r.PlaylistItem + r.Bookmarks +
		r.SkipSettings + r.WatchLater + r.WatchEvents + r.VideoTag
}

// OrphanCount 按白名单逻辑表名返回报告中的对应分区计数；非法表名返回错误。
// 供 handler 层在组装页面与校验清理入参时复用同一套白名单。
func (r OrphanReport) OrphanCount(table string) (int64, error) {
	if _, err := orphanPhysicalTable(table); err != nil {
		return 0, err
	}
	return *r.orphanField(table), nil
}

// IsOrphanTable 校验 table 是否为数据健康检查白名单内的逻辑表名。
// 供 handler 层在调用清理前提前拦截非法入参，底层删除方法仍会再校验一次。
func IsOrphanTable(table string) bool {
	_, err := orphanPhysicalTable(table)
	return err == nil
}

// notInClause 按 existing 构建 NOT IN 占位符片段与参数。
// 调用方须先确保 len(existing) > 0；空集合场景走无条件 SQL，避免 NOT IN (空集) 的全表误判。
func notInClause(existing map[string]bool) (string, []any) {
	placeholders := make([]string, 0, len(existing))
	args := make([]any, 0, len(existing))
	for id := range existing {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	return " NOT IN (" + strings.Join(placeholders, ",") + ")", args
}

// countOrphans 返回指定物理表的孤儿行数。existing 为空时回退为整表行数。
func (s *Store) countOrphans(physical string, existing map[string]bool) (int64, error) {
	var n int64
	if len(existing) == 0 {
		err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + physical).Scan(&n)
		return n, err
	}
	clause, args := notInClause(existing)
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM `+physical+` WHERE video_id`+clause, args...,
	).Scan(&n)
	return n, err
}

// deleteOrphanRows 删除指定物理表的孤儿行，返回实际删除行数。existing 为空时清空整表。
func (s *Store) deleteOrphanRows(physical string, existing map[string]bool) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if len(existing) == 0 {
		res, err = s.db.Exec(`DELETE FROM ` + physical)
	} else {
		clause, args := notInClause(existing)
		res, err = s.db.Exec(`DELETE FROM `+physical+` WHERE video_id`+clause, args...)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// reclaimEmptyTags 回收不再被任何视频引用的空标签，口径与 Store.Prune 完全一致。
func (s *Store) reclaimEmptyTags() error {
	_, err := s.db.Exec(
		`DELETE FROM tags WHERE id NOT IN (SELECT DISTINCT tag_id FROM video_tag)`)
	return err
}

// OrphanReport 统计七张表中指向已不存在视频的孤儿记录数。
// existing 为当前媒体库视频 ID 集合；空集合时各表回退为总行数（媒体库为空的安全口径）。
func (s *Store) OrphanReport(existing map[string]bool) (OrphanReport, error) {
	var report OrphanReport
	for _, t := range orphanTables {
		n, err := s.countOrphans(t.physical, existing)
		if err != nil {
			return OrphanReport{}, fmt.Errorf("统计 %s 孤儿记录失败: %w", t.physical, err)
		}
		*report.orphanField(t.logical) = n
	}
	return report, nil
}

// DeleteOrphansByTable 清理指定表的孤儿记录，返回实际删除行数。
// table 仅接受白名单逻辑表名，非法表名返回错误。video_tag 清理后顺带回收空标签；
// playlists 表头不在白名单内，永远不会被本方法删除。
func (s *Store) DeleteOrphansByTable(table string, existing map[string]bool) (int64, error) {
	physical, err := orphanPhysicalTable(table)
	if err != nil {
		return 0, err
	}
	removed, err := s.deleteOrphanRows(physical, existing)
	if err != nil {
		return 0, fmt.Errorf("清理 %s 孤儿记录失败: %w", physical, err)
	}
	if table == "video_tag" {
		if err := s.reclaimEmptyTags(); err != nil {
			return 0, fmt.Errorf("回收空标签失败: %w", err)
		}
	}
	return removed, nil
}

// DeleteAllOrphans 在单事务内清理全部七张表的孤儿记录，返回各表实际删除行数。
// 单连接串行写，事务保证「一键清理」要么全部成功要么整体不动。
func (s *Store) DeleteAllOrphans(existing map[string]bool) (OrphanReport, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OrphanReport{}, fmt.Errorf("开启清理事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var report OrphanReport
	for _, t := range orphanTables {
		var (
			res sql.Result
			err error
		)
		if len(existing) == 0 {
			res, err = tx.Exec(`DELETE FROM ` + t.physical)
		} else {
			clause, args := notInClause(existing)
			res, err = tx.Exec(`DELETE FROM `+t.physical+` WHERE video_id`+clause, args...)
		}
		if err != nil {
			return OrphanReport{}, fmt.Errorf("清理 %s 孤儿记录失败: %w", t.physical, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return OrphanReport{}, err
		}
		*report.orphanField(t.logical) = n
	}
	// 空标签回收与 video_tag 清理同事务提交
	if _, err := tx.Exec(
		`DELETE FROM tags WHERE id NOT IN (SELECT DISTINCT tag_id FROM video_tag)`); err != nil {
		return OrphanReport{}, fmt.Errorf("回收空标签失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OrphanReport{}, fmt.Errorf("提交清理事务失败: %w", err)
	}
	return report, nil
}

// OrphanSampleIDs 返回指定表中孤儿 video_id 的去重样例（最多 limit 个），供体检页人工核对。
// 表名走同一套白名单；existing 为空时返回整表的去重 video_id 样例。
func (s *Store) OrphanSampleIDs(table string, existing map[string]bool, limit int) ([]string, error) {
	physical, err := orphanPhysicalTable(table)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	var (
		rows *sql.Rows
	)
	if len(existing) == 0 {
		rows, err = s.db.Query(
			`SELECT DISTINCT video_id FROM `+physical+` LIMIT ?`, limit)
	} else {
		clause, args := notInClause(existing)
		args = append(args, limit)
		rows, err = s.db.Query(
			`SELECT DISTINCT video_id FROM `+physical+` WHERE video_id`+clause+` LIMIT ?`, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
