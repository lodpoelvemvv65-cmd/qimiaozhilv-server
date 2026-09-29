package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"mhqserver/internal/skillcatalog"
)

func (a *App) handleSkillCatalog(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if !hasPermission(session, "players.read") && !hasPermission(session, "players.skills") {
		writeError(w, http.StatusForbidden, rid, "forbidden", "没有技能查询权限")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	skills, err := skillcatalog.Load(ctx, a.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "skills_unavailable", "技能清单读取失败，请稍后重试")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": skills, "total": len(skills)}, nil)
}

func (a *App) attachSkillNames(ctx context.Context, items []map[string]any) {
	if a == nil || a.db == nil || len(items) == 0 {
		return
	}
	skills, err := skillcatalog.Load(ctx, a.db)
	if err != nil {
		return
	}
	names := skillcatalog.NamesByID(skills)
	for _, item := range items {
		id := skillIDFromResource(item["skill_id"])
		item["name"] = skillcatalog.DisplayName(id, names)
	}
}

func skillIDFromResource(value any) int32 {
	switch v := value.(type) {
	case int32:
		return v
	case int64:
		return int32(v)
	case float64:
		return int32(v)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
		if err != nil {
			return 0
		}
		return int32(n)
	default:
		return 0
	}
}
