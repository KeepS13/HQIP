package control

import (
	"errors"
	"net/http"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

// Announcement handlers keep admin audience data separate from user delivery.
// Authentication and CSRF are supplied by registerPortalAdmin/Public.
func announcementResult(w http.ResponseWriter, value any, err error) {
	switch {
	case err == nil:
		reply(w, http.StatusOK, value)
	case errors.Is(err, store.ErrAnnouncementInput):
		fail(w, 400, err.Error())
	case errors.Is(err, store.ErrNotFound):
		fail(w, 404, "公告不存在或已停用")
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "此发布标识已被使用，请重新创建公告")
	default:
		fail(w, 503, "公告服务暂不可用，请稍后重试")
	}
}

func (s *Server) adminAnnouncements(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var input model.AnnouncementInput
		if !readJSON(w, r, &input) {
			return
		}
		value, err := s.Store.PublishAnnouncement(input)
		announcementResult(w, value, err)
		return
	}
	value, err := s.Store.Announcements("", r.URL.Query().Get("before"))
	announcementResult(w, value, err)
}

func (s *Server) adminAnnouncement(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		announcementResult(w, map[string]bool{"ok": true}, s.Store.StopAnnouncement(r.PathValue("id")))
		return
	}
	value, err := s.Store.AnnouncementDetail(r.PathValue("id"))
	announcementResult(w, value, err)
}

func (s *Server) userAnnouncements(w http.ResponseWriter, r *http.Request, user, session string) {
	value, err := s.Store.Announcements(user, r.URL.Query().Get("before"))
	announcementResult(w, value, err)
}

func (s *Server) acknowledgeAnnouncement(w http.ResponseWriter, r *http.Request, user, session string) {
	announcementResult(w, map[string]bool{"ok": true}, s.Store.AcknowledgeAnnouncement(user, r.PathValue("id")))
}
