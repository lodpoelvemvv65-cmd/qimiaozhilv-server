package main

import (
	"log"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// portal.go：主城传送门 / 选图修复。
//
// 客户端传送点系统（HotfixView 反汇编确认）：
//   - 主城 10004 左右两个传送点（SceneTransConfig.TransPosArr = ±14.5）→ 点击后客户端
//     本地打开 LeftTransPointUI / RightTransPointUI 选图面板，每个按钮发送
//     C2M_RequestEnterMap{MapId}：
//       Left:  1000501(广场) / 1000601(海滩) / 1000701(荣耀殿堂) / 1000801(守护者公园)
//       Right: 1000901(挑战) / 1003001-1003801 / 1001001(Boss) 等
//   - 奇妙广场 10005 的时空旅人 NPC(1018) → 服务器推 M2C_OpenSpaceTravelNpcUI(20372)
//     → SpaceTravelNPCUI → SpaceTravelPointUI 选图（MapId = (10039+index)*100+1，
//     星空旅行场景，见 spacetravel.go）。
//
// 旧实现只接受主城/海滩层，其余目标一律"目标地图不存在" → 选图点击无反应。
// 现在：目标 sceneId 存在于 SceneTransConfig 即接受，出生点取目标场景 TransPosArr[0]。

// sceneSpawn 返回场景默认出生点（SceneTransConfig.TransPosArr 第一项；无配置回 0,0）。
// ⚠ 右光圈场景（光圈 x>0，如 Boss 10010 / 主线 10011 等只有右侧传送点）：
// 出生点 = 左侧镜像（玩家从主城左传送门进入 → 线上在场景左侧出生）。
// 若出生在右光圈 → 客户端立刻触发进入光圈 → 误触自动回城/换层（用户实测）。
// 左侧没有传送光圈，站左侧安全；左侧也避开字段怪（怪在光圈旁右侧）。
func sceneSpawn(sceneID int32) (float32, float32) {
	if tables == nil {
		return 0, 0
	}
	if row, ok := tables.sceneTrans[int64(sceneID)]; ok {
		arr := arrOf(row["TransPosArr"])
		if len(arr) > 0 {
			if p, ok := arr[0].(map[string]interface{}); ok {
				x := float32(numf(p["TransPos_x"]))
				y := float32(numf(p["TransPos_y"]))
				// TrialCopy's configured point is the town-facing portal on the
				// extreme left of the map. Spawning there leaves the player inside
				// the trigger, so the native client does not render the unit until
				// the player clicks or moves. Trial layers have no second entry
				// point; use the map centre and retain the configured safe Y.
				if sceneID == 10009 {
					return 0, y
				}
				if x > 0 {
					return -x, y // 右光圈 → 左侧镜像出生
				}
				// A few instances (notably TrialCopy) define their entry on the
				// left side. Returning the exact negative coordinate places the
				// player inside the transfer trigger and immediately sends them
				// back to the previous map. Move inward by the same portal offset
				// used by field monsters.
				if x < 0 {
					return x + portalOffset, y
				}
				return x, y
			}
		}
	}
	return 0, 0
}

// knownScene 判断 sceneId 是否在 SceneTransConfig 中（可传送目的地白名单）。
func knownScene(sceneID int32) bool {
	if tables == nil {
		return false
	}
	_, ok := tables.sceneTrans[int64(sceneID)]
	return ok
}

// 20043 → 20044：回主城。客户端各类"回城"按钮发送；服务器换图回 10004。
func (s *Server) onBackMainCity(ch *channel, req *protocol.C2M_BackMainCity) proto.Message {
	resp := &protocol.M2C_BackMainCity{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ch.session.stopMainStoryAI()
	allowed, message := s.returnPartyToMainCity(ch, "back-main-city", false, true)
	if !allowed {
		resp.Message = message
		log.Printf("[S=%d] reject back main city player=%d: %s", ch.id, ch.session.playerID, message)
		return resp
	}
	log.Printf("[S=%d] back main city from map=%d", ch.id, ch.session.mapID)
	return resp
}

// 20031 → 20032 回主城坐标：主城配置按 +14.5、-14.5 的顺序定义两个传送点。
// 客户端实际场景的屏幕方向与 X 轴相反：负 X 是画面左侧，正 X 是画面右侧。
// ⚠ 出生点不能站在光圈正中央：客户端 TransPointComponent.OnEnterTransPoint 在角色
// 处于光圈时触发，遍历传送点 HashSet 时若光圈被重建（20039 重启光圈）→ 枚举中修改
// 集合 → InvalidOperationException → 客户端崩溃掉线（Player.log 实证）。因此偏移
// portalOffset 到光圈【旁边】（与字段怪同理），既不触发进入光圈也不误触传送。
func mainCitySpawn(layer int32) (float32, float32) {
	if layer == 1 {
		return -14.50656 + portalOffset, -1.474469 // 画面左侧入口旁（向场内）
	}
	return 14.50656 - portalOffset, -1.474469 // 画面右侧入口旁（向场内）
}

func mainCityReturnSpawn() (float32, float32) {
	return mainCitySpawn(1)
}
