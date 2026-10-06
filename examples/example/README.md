# 示例插件

插件开发入门示例：at 机器人发送 `/example`，机器人回复问候语。

## 功能

- 群聊/私聊中 at 机器人并发送 `/example`，回复 `Hello, AniaBot!`

## 命令

| 命令 | 说明 |
| --- | --- |
| `/example` | 触发问候语（需 at 机器人） |

## 开发参考

本插件展示了：

- `plugin.Meta` 嵌入与元信息声明（`Name` / `HelpWords` / `ShowFor` / `Order` 等）
- 消息事件 `OnGroupMsg` / `OnFriendMsg` 的返回语义（返回 `false` 停止后续插件）
- `msgchain.Builder()` 构造回复消息
- 卸载钩子 `OnUnload`（可选接口 `plugin.UnloadEvent`）：区分退出/重启与市场卸载两种原因
- 无第三方依赖，只用框架公共 API

## 说明

插件默认支持所有平台；如果只支持 QQ，可将 `plugin.json` 的 `platforms` 改为 `["qq"]`。
