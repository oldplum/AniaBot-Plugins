# plugin.json 规范

> 开发插件前建议先阅读 AniaBot 的[插件系统概览](https://aniabot-project.github.io/AniaBot/plugin/overview)与[第一个插件](https://aniabot-project.github.io/AniaBot/plugin/first-plugin)；本文档只规定市场插件的元信息（plugin.json）格式。

每个插件目录 `plugins/<id>/` 必须包含 `plugin.json`、`README.md` 与 Go 源码。

## 目录与 ID 约束

- 插件 ID 只允许小写字母、数字、`-`、`_`，长度 2~64
- 安装后插件源码位于 AniaBot 源码树 `custom/plugins/<id>`，import 路径固定为：
  `github.com/jeanhua/AniaBot/custom/plugins/<id>`
- 插件**不带自己的 go.mod**，作为 AniaBot 主模块的内包编译；如需第三方依赖，直接在源码中 import，安装流水线的 `go mod tidy` 会自动拉取并写入 AniaBot 的 go.mod

## 字段定义

```json
{
  "id": "example",
  "name": "示例插件",
  "description": "一句话介绍，展示在市场列表",
  "author": "jeanhua",
  "version": "1.0.0",
  "platforms": ["qq"],
  "tags": ["示例"],
  "api_version": 1,
  "min_framework": "4.6.0",
  "entry": {
    "constructor": "NewPlugin"
  },
  "icon": ""
}
```

| 字段 | 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `id` | 是 | string | 插件唯一 ID，须与目录名一致 |
| `name` | 是 | string | 插件显示名称 |
| `description` | 是 | string | 一句话简介（面板列表展示） |
| `author` | 是 | string | 作者（GitHub 用户名） |
| `version` | 是 | string | 插件版本，建议语义化版本 |
| `platforms` | 否 | string[] | 支持的平台标识（`qq`/`feishu`/`telegram`/`discord`/`qqofficial`），空 = 全部 |
| `tags` | 否 | string[] | 分类标签，用于市场筛选 |
| `api_version` | 否 | int | 插件 API 版本，当前为 1；与 AniaBot 不兼容时安装会被拒绝 |
| `min_framework` | 否 | string | 要求的最低 AniaBot 版本（语义化比较） |
| `entry.constructor` | 否 | string | 插件构造函数名，默认 `NewPlugin`，返回 `plugin.Plugin` |
| `icon` | 否 | string | 图标文件名（可选，`png`/`svg`，存放在插件目录内） |

## 插件源码要求

- 包名任意，但构造函数（默认 `NewPlugin`）必须返回 `common/plugin.Plugin`（嵌入 `plugin.Meta` 实现）
- 元信息 `Meta.Name` 建议与 `plugin.json` 的 `name` 一致（面板已安装列表以此展示）
- 可选的扩展能力照常生效：`ConfigSchema()` 让面板「配置管理」自动渲染表单；`ConfigRegistrar` 动态注册配置字段；`PlatformEventHandler` 接收平台专属事件；`InteractionHandler` 接收内联按钮点击；`UnloadEvent`（`OnUnload`）在插件卸载前清理资源与数据；`OnPanic` 处理运行期 panic
- 实现 `UnloadEvent` 的插件必须把 `min_framework` 抬到 `4.7.7` 及以上（更早的框架没有该接口）
- 禁止在 `init()` 中做网络请求/读写文件等有副作用操作（安装编译期会被执行）

## 示例

完整示例见 [examples/example](../examples/example)（示例位于 `examples/`，仅作开发参考，不进入插件市场）。核心骨架：

```go
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

type ExamplePlugin struct {
    plugin.Meta
}

func NewPlugin() *ExamplePlugin {
    p := &ExamplePlugin{}
    p.Name = "示例插件"
    p.HelpWords = "示例插件：at 我发送 /example 触发"
    p.AdminOnly = false
    p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
    p.Author = "jeanhua"
    p.Version = "1.0.0"
    p.Order = plugin.LevelNormal
    return p
}

func (p *ExamplePlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if !cmd.Mention || cmd.Name != "example" {
        return true, nil
    }
    builder := msgchain.Builder().Group()
    builder.Text("Hello, AniaBot!")
    b.SendGroupMsg(msg.GroupId, builder.Build())
    return false, nil
}
```
