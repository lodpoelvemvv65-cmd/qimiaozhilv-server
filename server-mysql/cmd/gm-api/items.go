package main

import (
	"bytes"
	"net/http"
	"net/url"

	"mhqserver/internal/itemcatalog"
)

func (a *App) handleItemCatalog(w http.ResponseWriter, r *http.Request, rid string, session *sessionData, export bool) {
	if !hasPermission(session, "players.read") && !hasPermission(session, "players.items") && !hasPermission(session, "players.mail") {
		writeError(w, http.StatusForbidden, rid, "forbidden", "没有物品查询权限")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	items, err := itemcatalog.Load(ctx, a.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "items_unavailable", "物品清单读取失败，请稍后重试")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if export {
		var buf bytes.Buffer
		if err := itemcatalog.WriteCSV(&buf, items); err != nil {
			writeError(w, http.StatusInternalServerError, rid, "export_failed", "物品清单导出失败")
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=gm-items.csv; filename*=UTF-8''"+url.PathEscape("GM物品ID中文清单.csv"))
		_, _ = w.Write(buf.Bytes())
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items, "total": len(items)}, nil)
}
