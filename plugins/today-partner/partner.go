// Package todaypartner 今日对象插件：群内抽取今日老婆/老公/对象，或强娶指定群友。
//
// 本文件只放与框架无关的纯逻辑（状态迁移、候选筛选、@ 目标解析、冷却计算），
// 便于表驱动单测；与 bot/存储交互的部分在 plugin.go。
package todaypartner

import (
	"fmt"
	"math/rand"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// partner 一个今日对象记录。
type partner struct {
	QQ   string `json:"qq"`   // 裸 QQ 号（用于拼头像 URL）
	Name string `json:"name"` // 展示名（群名片优先，回退昵称）
}

// drawState 一个用户在一个群的当日抽取状态。
type drawState struct {
	Date     string    `json:"date"`     // 状态所属日期（2006-01-02），跨天自动重置
	Used     int       `json:"used"`     // 今日已抽取次数
	Partners []partner `json:"partners"` // 今日抽到的对象（按抽取顺序累积）
}

// ensureToday 跨天重置：状态不是今天的就清零（同键复用，无历史垃圾数据）。
func (s *drawState) ensureToday(today string) {
	if s.Date != today {
		*s = drawState{Date: today}
	}
}

// remaining 今日剩余抽取次数。
func (s *drawState) remaining(maxDraws int) int {
	left := clampDraws(maxDraws) - s.Used
	if left < 0 {
		return 0
	}
	return left
}

// clampDraws 每日抽取次数限制在 1-5。
func clampDraws(n int) int {
	if n < 1 {
		return 1
	}
	if n > 5 {
		return 5
	}
	return n
}

// drawnSet 今日已抽到的 QQ 集合（从候选里剔除用）。
func (s *drawState) drawnSet() map[string]bool {
	set := make(map[string]bool, len(s.Partners))
	for _, pt := range s.Partners {
		set[pt.QQ] = true
	}
	return set
}

// pickCandidate 从候选中随机挑一个（调用方已负责剔除自己/机器人/今日已抽到的人）。
// 候选为空时返回 false。
func pickCandidate(candidates []partner) (partner, bool) {
	if len(candidates) == 0 {
		return partner{}, false
	}
	return candidates[rand.Intn(len(candidates))], true
}

// filterCandidates 把群成员列表整理成去重候选：剔除发送者本人、机器人、
// 机器人账号（IsRobot）与今日已抽到的人；展示名取群名片，为空回退昵称。
func filterCandidates(members []message.GroupUserInfo, self, sender message.QID, drawn map[string]bool) []partner {
	seen := make(map[string]bool)
	out := make([]partner, 0, len(members))
	for i := range members {
		qq := members[i].UserID.TrimQQPrefix()
		if qq == "" || seen[qq] {
			continue
		}
		if members[i].UserID == self || members[i].UserID == sender || members[i].IsRobot || drawn[qq] {
			continue
		}
		seen[qq] = true
		name := members[i].Card
		if name == "" {
			name = members[i].Nickname
		}
		if name == "" {
			name = "QQ" + qq
		}
		out = append(out, partner{QQ: qq, Name: name})
	}
	return out
}

// mentionTargets 提取消息里所有 at 目标（剔除 @全体成员），保持出现顺序。
// at 段的 qq 值已由框架规范化为带 qq: 前缀的 QID。
func mentionTargets(segs []message.OB11Segment) []message.QID {
	out := make([]message.QID, 0, 2)
	for _, seg := range segs {
		if seg.Type != message.SegmentMention {
			continue
		}
		qq, _ := seg.Data["qq"].(string)
		if qq == "" || qq == "all" {
			continue
		}
		out = append(out, message.QID(qq))
	}
	return out
}

// marryTarget 从 at 目标里解析强娶对象：跳过发送者自己，取第一个非机器人的目标。
// 返回值 ok 取值：0 = 没有有效目标（提示用法）；1 = 只 @ 了机器人（吐槽）；
// 2 = 解析成功。
func marryTarget(segs []message.OB11Segment, self, sender message.QID) (target message.QID, ok int) {
	atedBot := false
	for _, qid := range mentionTargets(segs) {
		if qid == sender {
			continue
		}
		if qid == self {
			atedBot = true
			continue
		}
		return qid, 2
	}
	if atedBot {
		return "", 1
	}
	return "", 0
}

// avatarURL QQ 头像直链（qlogo 公共 CDN，无需 rkey 签名）。
func avatarURL(qq string) string {
	return fmt.Sprintf("https://q1.qlogo.cn/g?b=qq&nk=%s&s=640", qq)
}

// marryCooldownSec 强娶冷却剩余秒数；<=0 表示可以强娶（cooldownMin <= 0 视为不限制）。
func marryCooldownSec(last, now int64, cooldownMin int) int64 {
	if cooldownMin <= 0 || last <= 0 || now <= last {
		return 0
	}
	wait := last + int64(cooldownMin)*60 - now
	if wait < 0 {
		return 0
	}
	return wait
}

// partnerKind 一种命令对应的对象称谓与代词。
type partnerKind struct {
	Label   string // 今日老婆 / 今日老公 / 今日对象
	Pronoun string // 她 / 他 / TA
}

// kindOf 命令名 → 称谓与代词。
func kindOf(cmdName string) partnerKind {
	switch cmdName {
	case "今日老公", "抽老公":
		return partnerKind{Label: "今日老公", Pronoun: "他"}
	case "今日对象":
		return partnerKind{Label: "今日对象", Pronoun: "TA"}
	default: // 今日老婆 / 抽老婆
		return partnerKind{Label: "今日老婆", Pronoun: "她"}
	}
}
