// Package example 是插件市场示例插件：at 机器人发送 /example 回复问候语。
package example

import (
	"context"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// ExamplePlugin 插件定义：嵌入 plugin.Meta 获得默认实现，只需覆盖需要的方法。
type ExamplePlugin struct {
	plugin.Meta
}

// NewPlugin 构造函数（plugin.json 的 entry.constructor 默认指向这里）。
func NewPlugin() *ExamplePlugin {
	p := &ExamplePlugin{}
	p.Name = "示例插件"
	p.HelpWords = "at 我发送 /example 触发问候"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.1"
	p.Order = plugin.LevelNormal
	return p
}

// OnGroupMsg 群聊消息事件：返回 (是否继续传播, 错误)。
func (p *ExamplePlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !cmd.Mention || cmd.Name != "example" {
		return true, nil
	}
	builder := msgchain.Builder().Group()
	builder.Text("Hello, AniaBot!")
	b.SendGroupMsg(msg.GroupId, builder.Build())
	// 返回 false：本插件已处理，不再向后续插件传播
	return false, nil
}

// OnFriendMsg 私聊消息事件：与群聊一致。
func (p *ExamplePlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if cmd.Name != "example" {
		return true, nil
	}
	builder := msgchain.Builder().Friend()
	builder.Text("Hello, AniaBot!")
	b.SendFriendMsg(msg.Sender.UserId, builder.Build())
	return false, nil
}

// OnUnload 卸载钩子（可选接口 plugin.UnloadEvent）：插件被卸载前执行一次清理。
// reason 为 plugin.UnloadShutdown（Bot 退出/重启，全部插件都会收到）或
// plugin.UnloadUninstall（插件市场卸载，仅被卸载的插件收到）。
//
// 有状态插件应在这里释放资源：取消后台 goroutine、清空内存缓存；
// 只有 UnloadUninstall 才应删除持久化数据（UnloadShutdown 后插件会重新加载）。
// 钩子可能与运行期事件并发，注意并发安全与 1 分钟超时。
func (p *ExamplePlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	if reason == plugin.UnloadUninstall {
		// 示例：清空本插件的持久化数据（如 p.PersistentStorage.Clear(ctx)）
	}
	return nil
}
