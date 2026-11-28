package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// ratingRequest 是评分写入请求体，stars 取 1-5。
type ratingRequest struct {
	Stars int `json:"stars"`
}

// handleRatingPut 写入或覆盖某视频的个人评分。
func (s *Server) handleRatingPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	var req ratingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if req.Stars < 1 || req.Stars > 5 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stars 必须在 1-5 之间"})
		return
	}
	if err := s.store.SetRating(id, req.Stars); err != nil {
		log.Printf("保存评分失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存评分失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "stars": req.Stars})
}

// handleRatingGet 查询某视频的当前评分，无记录返回 stars=0。
func (s *Server) handleRatingGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	stars := 0
	if rt, err := s.store.GetRating(id); err != nil {
		log.Printf("查询评分失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询评分失败"})
		return
	} else if rt != nil {
		stars = rt.Stars
	}
	writeJSON(w, http.StatusOK, map[string]any{"videoId": id, "stars": stars})
}

// handleRatingDelete 清除某视频的评分。
func (s *Server) handleRatingDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	if err := s.store.DeleteRating(id); err != nil {
		log.Printf("清除评分失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "清除评分失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// highRatedCard 是「我的高分」页的卡片，预计算星级展示串。
type highRatedCard struct {
	Video    *media.Video
	Metadata *media.Metadata
	Rating   store.Rating
	StarsTxt string // 如「★★★★☆」，模板不做循环外的字符串拼装
}

// highRatedPageData 是「我的高分」页模板数据。
type highRatedPageData struct {
	basePage
	StoreOn bool
	Cards   []highRatedCard
}

// handleHighRatedPage 渲染个人 4 星及以上的评分视图，按星级/评分时间倒序。
func (s *Server) handleHighRatedPage(w http.ResponseWriter, r *http.Request) {
	if !s.storeEnabled() {
		s.render(w, "high_rated.html", highRatedPageData{
			basePage: basePage{Title: "我的高分", AuthEnabled: s.authEnabled()},
			StoreOn:  false,
		})
		return
	}
	ratings, err := s.store.HighRated(4, 100)
	if err != nil {
		log.Printf("查询高分列表失败: %v", err)
		ratings = nil
	}
	cards := make([]highRatedCard, 0, len(ratings))
	for _, rt := range ratings {
		v, ok := s.library.Get(rt.VideoID)
		if !ok {
			continue // 视频已被移除，跳过悬空评分
		}
		cards = append(cards, highRatedCard{
			Video:    v,
			Metadata: s.library.Metadata(v.ID),
			Rating:   rt,
			StarsTxt: starsText(rt.Stars),
		})
	}
	s.render(w, "high_rated.html", highRatedPageData{
		basePage: basePage{Title: "我的高分", AuthEnabled: s.authEnabled()},
		StoreOn:  true,
		Cards:    cards,
	})
}

// starsText 把 1-5 星转成 ★/☆ 展示串（UTF-8 字符串，不能按 byte 拼接）。
func starsText(stars int) string {
	if stars < 0 {
		stars = 0
	}
	if stars > 5 {
		stars = 5
	}
	return strings.Repeat("★", stars) + strings.Repeat("☆", 5-stars)
}
