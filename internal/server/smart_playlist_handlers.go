package server

import (
	"net/http"
	"net/url"
	"sort"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// 智能清单是无状态的实时规则视图：不落库、不可手工增删条目，
// 每次请求按当前进度/收藏/标签数据现算，因此与普通播放列表完全解耦，零 schema 变更。

// smartRule 描述一条内置规则。Key 出现在 URL 上，必须严格白名单匹配。
type smartRule struct {
	Key         string
	Name        string
	Description string
	StoreNeeded bool // 是否依赖持久化数据（未开启 SQLite 时不展示、不可访问）
}

// smartRules 保持固定展示顺序。tag 规则不直接出现在中心页，
// 其卡片由 handleSmartPlaylists 按用户标签动态生成，但 key 必须注册进白名单以通过路由校验。
var smartRules = []smartRule{
	{Key: "unwatched", Name: "未观看", Description: "媒体库中从未产生播放记录的视频", StoreNeeded: false},
	{Key: "continue", Name: "继续观看", Description: "看到一半、还没看完的视频，按最近观看排序", StoreNeeded: true},
	{Key: "favorites", Name: "我的收藏", Description: "所有已加星收藏的视频", StoreNeeded: true},
	{Key: "tag", Name: "标签", Description: "带有指定标签的全部视频", StoreNeeded: true},
}

// smartRuleIndex 供 O(1) 白名单查找。
var smartRuleIndex = func() map[string]smartRule {
	m := make(map[string]smartRule, len(smartRules))
	for _, r := range smartRules {
		m[r.Key] = r
	}
	return m
}()

// smartRuleCard 是中心页的一张规则卡片（含实时计数与预生成的详情链接）。
type smartRuleCard struct {
	smartRule
	Count int
	URL   string // 中心页直接渲染，避免模板里做字符串裁剪/转义
}

// smartData 是智能清单中心页与规则详情页共用的视图模型。
type smartData struct {
	basePage
	StoreOn  bool
	Rules    []smartRuleCard // 中心页：规则卡片
	Detail   bool            // 是否为规则详情视图
	Rule     smartRule       // 详情视图下的当前规则
	Videos   []*media.Video
	Metadata map[string]*media.Metadata
	State    videoState
}

// handleSmartPlaylists 渲染智能清单中心：列出内置规则与每个用户标签规则，附实时计数。
func (s *Server) handleSmartPlaylists(w http.ResponseWriter, r *http.Request) {
	state := s.loadVideoState()
	data := smartData{
		basePage: basePage{Title: "智能清单", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
		State:    state,
		Metadata: map[string]*media.Metadata{},
	}

	for _, rule := range smartRules {
		if rule.Key == "tag" || (rule.StoreNeeded && !s.storeEnabled()) {
			continue // tag 卡片由下方标签循环生成；store 依赖规则在无持久化时隐藏
		}
		videos := s.collectSmartRule(rule.Key, "", state)
		data.Rules = append(data.Rules, smartRuleCard{
			smartRule: rule,
			Count:     len(videos),
			URL:       "/smart/" + rule.Key,
		})
	}

	// 每个用户标签自动成为一条动态规则（仅持久化开启时存在）
	if s.storeEnabled() {
		if tags, err := s.store.ListTags(); err == nil {
			for _, tg := range tags {
				set, err := s.store.TagVideoSet(tg.ID)
				if err != nil {
					continue
				}
				data.Rules = append(data.Rules, smartRuleCard{
					smartRule: smartRule{
						Key:         "tag",
						Name:        "标签 · " + tg.Name,
						Description: "带有「" + tg.Name + "」标签的全部视频",
						StoreNeeded: true,
					},
					Count: len(set),
					URL:   "/smart/tag?name=" + url.QueryEscape(tg.Name),
				})
			}
		}
	}

	s.render(w, "smart_playlists.html", data)
}

// handleSmartPlaylistDetail 渲染单条规则的视频网格；rule 与 tag 参数均做白名单/存在性校验。
func (s *Server) handleSmartPlaylistDetail(w http.ResponseWriter, r *http.Request) {
	ruleKey := r.PathValue("rule")
	rule, ok := smartRuleIndex[ruleKey]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if rule.StoreNeeded && !s.storeEnabled() {
		http.NotFound(w, r)
		return
	}

	tagName := ""
	if ruleKey == "tag" {
		if !s.storeEnabled() {
			http.NotFound(w, r)
			return
		}
		tagName = r.URL.Query().Get("name")
		tg, err := s.store.TagByName(tagName)
		if err != nil || tg == nil {
			http.NotFound(w, r) // 标签已删除或伪造：按不存在处理，不静默回退
			return
		}
	}

	state := s.loadVideoState()
	videos := s.collectSmartRule(ruleKey, tagName, state)
	metaMap := make(map[string]*media.Metadata, len(videos))
	for _, v := range videos {
		if md := s.library.Metadata(v.ID); md != nil {
			metaMap[v.ID] = md
		}
	}
	if tagName != "" {
		rule.Name = "标签 · " + tagName
		rule.Description = "带有「" + tagName + "」标签的全部视频"
	}

	s.render(w, "smart_playlists.html", smartData{
		basePage: basePage{Title: rule.Name + " · 智能清单", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
		Detail:   true,
		Rule:     rule,
		Videos:   videos,
		Metadata: metaMap,
		State:    state,
	})
}

// collectSmartRule 按规则实时收集视频。state 由调用方一次性加载，避免页内重复查库。
func (s *Server) collectSmartRule(ruleKey, tagName string, state videoState) []*media.Video {
	all := s.library.List()
	out := make([]*media.Video, 0)

	switch ruleKey {
	case "unwatched":
		for _, v := range all {
			if _, watched := state.Progress[v.ID]; !watched {
				out = append(out, v)
			}
		}
		// List 已按 RelPath 排序，未观看保持稳定的路径顺序
	case "continue":
		// 复用继续观看同口径：位置 >5s 且未看到 95%；按进度更新时间倒序
		type candidate struct {
			v *media.Video
			p store.Progress
		}
		var cands []candidate
		for _, v := range all {
			p, ok := state.Progress[v.ID]
			if ok && hasProgressValue(p.Position, p.Duration) {
				cands = append(cands, candidate{v, p})
			}
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].p.UpdatedAt.After(cands[j].p.UpdatedAt) })
		for _, c := range cands {
			out = append(out, c.v)
		}
	case "favorites":
		for _, v := range all {
			if state.Favs[v.ID] {
				out = append(out, v)
			}
		}
	case "tag":
		tg, err := s.store.TagByName(tagName)
		if err != nil || tg == nil {
			return out
		}
		set, err := s.store.TagVideoSet(tg.ID)
		if err != nil {
			return out
		}
		for _, v := range all {
			if set[v.ID] {
				out = append(out, v)
			}
		}
	}
	return out
}
