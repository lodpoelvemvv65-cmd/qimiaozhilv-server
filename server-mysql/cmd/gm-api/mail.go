package main

import (
	"context"
	"errors"
	"fmt"

	json "github.com/goccy/go-json"
	"mhqserver/internal/itemcatalog"
)

type mailAttachment struct {
	ItemID int32 `json:"itemId"`
	Count  int32 `json:"count"`
}

func parseMailAttachments(raw json.RawMessage) ([]mailAttachment, error) {
	var payload struct {
		Items []mailAttachment `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("邮件附件参数错误，物品 ID 和数量必须为整数")
	}
	if len(payload.Items) > 100 {
		return nil, errors.New("每封邮件最多添加 100 项附件")
	}
	for _, item := range payload.Items {
		if item.ItemID <= 0 || item.Count <= 0 || item.Count > 1000000000 {
			return nil, errors.New("邮件附件 ID 必须为正整数，数量必须为 1 到 1,000,000,000 的整数")
		}
	}
	return payload.Items, nil
}

func (a *App) validateMailAttachments(ctx context.Context, raw json.RawMessage) error {
	items, err := parseMailAttachments(raw)
	if err != nil || len(items) == 0 {
		return err
	}
	catalog, err := itemcatalog.Load(ctx, a.db)
	if err != nil {
		return errors.New("物品清单读取失败，邮件未发送，请稍后重试")
	}
	available := make(map[int32]bool, len(catalog))
	for _, item := range catalog {
		available[item.ID] = true
	}
	for _, item := range items {
		if !available[item.ItemID] {
			return fmt.Errorf("物品 ID %d 不存在，请重新搜索选择", item.ItemID)
		}
	}
	return nil
}
