# 群聊看板（groupdashboard）

群消息累计达到一定数量后，自动调用 AI 把聊天记录整理成一份「**群聊日常分析看板**」，渲染成精美长图发到群里。是群刊（groupdigest）的看板增强版：消息量、参与人数等统计数字由本地真实计算，话题、画像、金句等内容由 AI 提炼。

> 与「群刊」插件功能重叠，**不要在同一个群同时启用两个插件**。

## 功能

- 在配置的群聊中累计消息数量，达到阈值后自动生成一期看板
- 看板内容（自上而下）：
  - **统计概览**：消息总量 / 参与人数 / 热门话题数 / 字符总数（本地真实统计）
  - **统计时段横幅 + 24 小时活动柱状图**（本地统计）
  - **话题焦点**：3~5 个热点话题，含描述、代表性发言与标签
  - **群友画像**：活跃群友的人设称号、印象描述与发言条数
  - **今日金句**：金句原文 + 编辑毒舌/幽默点评（聊天气泡样式）
  - **群聊氛围报告**：活跃度、话题集中度等维度评分
  - **编辑寄语**
- 三种看板风格可选：`mint` 薄荷看板（默认）/ `magazine` 杂志海报 / `dark` 暗夜霓虹
- 头像展示：QQ 用户自动使用 QQ 头像（渲染看板时由本机 md2img 服务访问 `q1.qlogo.cn` 拉取）；其他平台或头像加载失败时显示昵称首字圆标
- 两种发送形式：
  - `image`（默认）：渲染看板 HTML 后经本地 [md2img-api](https://hub.docker.com/r/jeanhua/md2img-api) 容器截图成 PNG 发送
  - `md`：发送 Markdown 文本文件（无需额外服务，样式降级为纯文本排版）
- 支持冷却时间，避免群内频繁生成
- **状态持久化**：计数与最近消息自动落盘（SQLite/MySQL），重启后进度不丢

> 系统提示词已内置在代码中（要求 AI 输出结构化 JSON），无需也无法在配置中修改。

## 管理命令

群内命令需 @ 机器人；**私聊命令无需艾特**（直接发送斜杠命令即可，如 `/dashboard all`）。命令本身不计入看板计数：

| 命令 | 说明 |
| --- | --- |
| `/dashboard status`（或 `/看板状态`） | 查看当前收集进度：计数/阈值、缓冲消息数、是否生成中、最近生成时间、冷却剩余 |
| `/dashboard list [n]`（或 `/看板列表`） | 查看最近 n 条已收集消息（默认 10，上限 50） |
| `/dashboard clear`（或 `/看板清空`） | 清空当前群的计数与消息缓冲，重新开始收集 |
| `/dashboard all`（或 `/看板全部`） | **管理员**：查看所有作用群的收集状态（**私聊**或群聊均可） |
| `/dashboard now`（或 `/看板立即生成`） | **管理员**：在当前群立即用已收集消息生成一期看板（绕过阈值与冷却） |

> 管理员为面板 `bot.admin_id` 配置的账号。

## 配置

在面板「配置管理」中配置（修改后需重启 Bot 生效）：

| 配置键 | 说明 | 默认值 |
| --- | --- | --- |
| `plugin.groupdashboard.enable` | 是否启用看板 | `true` |
| `plugin.groupdashboard.group_ids` | 作用群聊列表（逗号分隔） | 空（不对任何群生效） |
| `plugin.groupdashboard.threshold` | 触发消息数 | `120` |
| `plugin.groupdashboard.max_messages` | 喂给 AI 的最大消息数 | `200` |
| `plugin.groupdashboard.send_mode` | 发送形式：`image` / `md` | `image` |
| `plugin.groupdashboard.style` | 看板风格：`mint` / `magazine` / `dark`（仅 image 模式生效） | `mint` |
| `plugin.groupdashboard.md2img_url` | md2img 渲染服务地址 | `http://127.0.0.1:3000` |
| `plugin.groupdashboard.cooldown_minutes` | 生成冷却（分钟），`0` 不限制 | `0` |

### 群 ID 写法

- **QQ**：直接填群号（如 `123456`）或带 `qq:` 前缀（`qq:123456`），二者等价
- **飞书**：`fs:oc_xxx`
- **Telegram**：`tg:-100xxxx`
- **Discord**：`dc:频道ID`
- **QQ 官方**：`qo:群开放ID`

> 群 ID 可从面板日志或 AI 对话的会话 ID（`g:<群ID>`）中查看。

## 依赖

- **AI 对话插件**：看板只复用其**连接信息**——`plugin.ai_chat_bot.base_url` / `api_key` / `model` / `api_format`。
  其余参数（重试、备用模型、采样、输出上限、Prompt 缓存等）**一律不继承**。
  未配置 API Key 时插件仍可安装运行，但不会生成看板（日志会提示）。
- **md2img-api**（仅 `image` 模式）：需先在本地启动容器：

```bash
docker run -d -p 3000:3000 --name md2img-api jeanhua/md2img-api:latest
```

服务地址在 `plugin.groupdashboard.md2img_url` 配置（默认 `http://127.0.0.1:3000`）。
image 模式下 md2img 内置浏览器会访问 `q1.qlogo.cn` 拉取 QQ 头像，除此之外无其他外部请求。

## 行为说明

- 插件只统计**作用群列表**中的群消息；达到阈值后异步生成（不阻塞消息处理），生成期间新消息继续累计
- 每次生成使用独立的 AI 上下文（生成前清空历史），互不干扰
- AI 要求只输出 JSON；若首次输出不是合法 JSON，会在同一会话内自动追问重试一次，仍失败则向群内发送失败提示并记录日志
- image 模式下若 md2img 服务不可用，会向群内发送失败提示并记录日志；可临时切换 `send_mode=md` 降级使用
- 消息量越大内容越丰富，建议阈值不低于 100 条；阈值过低时 AI 提炼的话题/金句会较少

## 平台支持

QQ / 飞书 / Telegram / Discord / QQ 官方均支持。QQ 头像仅 QQ 平台显示，其他平台自动使用首字圆标。
