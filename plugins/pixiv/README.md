# Pixiv

登录 Pixiv 后的全功能插画插件：搜索、排行榜、推荐、作品/画师查询、相关作品，内置分级（R18）过滤、个人限流与群放行名单。

## 功能与命令

群聊需 @机器人，私聊直接发。`<必填>`、`[可选]`。

| 命令 | 说明 |
| --- | --- |
| `/pixiv 搜索 <关键词> [页码]` | 关键词搜索插画（tag 部分匹配，按时间倒序） |
| `/pixiv 图 <作品ID> [页码]` | 查看作品详情并发图；多图作品可指定页码翻页 |
| `/pixiv 排行 [日\|周\|月] [r18]` | 插画排行榜，如 `/pixiv 排行 周 r18` |
| `/pixiv 推荐` | 为登录账号个性化推荐 |
| `/pixiv 画师 <用户ID> [页码]` | 画师近期插画作品（第 1 页附画师统计） |
| `/pixiv 相关 <作品ID>` | 相似作品推荐 |
| `/pixiv 状态` | 登录状态、内容分级、使用额度 |
| `/pixiv 帮助` | 帮助 |

小技巧：`/pixiv 少女前线` 等价于 `/pixiv 搜索 少女前线`；作品 ID / 用户 ID 就是 pixiv 网页链接里的数字（`artworks/12345`、`users/678`）。搜索/排行等列表命令默认附 1 张预览图，想看大图用 `/pixiv 图 <ID>`。

## 登录配置（不填无法使用）

插件使用 Pixiv App API（OAuth）。在面板「配置管理 → Pixiv」填入 `Refresh Token` 即可：

1. 用开源工具获取 refresh_token，例如 [gppt](https://github.com/eggplants/get-pixivpy-token)：

   ```bash
   pip install gppt
   gppt login
   ```

   成功后输出 JSON 里的 `refresh_token` 字段即所需值（长期有效）。其他同类工具均可，能拿到 refresh_token 就行；登录如遇人机验证按工具提示完成。
2. 粘贴到面板保存即可。插件会自动换取并续期 access_token（约每小时一次），无需人工干预；refresh_token 一般不会变，配置一次长期使用。

> 建议：使用小号登录；bot 高频访问有触发 Pixiv 风控的可能。新注册账号部分功能可能受限。

## 网络代理（国内必须）

`proxy` 支持 `http://` 与 `socks5://` 两种写法（如 `socks5://127.0.0.1:7890`），API 请求与图片下载都走该代理；留空直连。国内网络直连 Pixiv 不可达，不配代理会全部超时。

## 内容分级

`content_type` 三档：

- `safe`（默认）：仅全年龄作品，请求 R18 榜单会被拒绝
- `mixed`：不过滤
- `r18`：仅 R18 作品（排行榜自动切换到对应的 R18 榜）

群聊使用建议配合 `allow_groups` 放行名单，避免在不合适的群里出现 R18 内容。

## 其余配置

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `enable` | true | 关闭后不响应任何 /pixiv 指令 |
| `list_size` | 5 | 列表条目数（1~10） |
| `preview_count` | 1 | 列表附带预览图张数（0~3） |
| `cooldown_sec` | 20 | 个人冷却秒数 |
| `daily_limit` | 30 | 每人每日请求限量（0 不限） |
| `allow_groups` / `allow_friends` | 空 | 正则放行名单，留空=不放行，`.*` 全放行 |
| `admin_bypass` | true | 管理员旁路限流与名单 |
| `silent_deny` | false | 非放行会话保持沉默（不回复） |

## 网络请求说明

本插件只访问 Pixiv 官方域名：`app-api.pixiv.net`（数据接口）、`oauth.secure.pixiv.net`（登录换 token）、`i.pximg.net`（图片下载）。除此之外不与任何第三方服务通信，refresh_token 等凭证只用于向 Pixiv 官方换取登录态，不会外发。

## 卸载说明

- 从插件市场卸载时会清空登录会话与频率缓存；Bot 退出/重启时这些状态随进程释放，重启后按 refresh_token 重新登录。

## 常见问题

- **提示登录失败 / refresh_token 已失效**：重新获取 refresh_token 并在面板更新。
- **连接超时**：检查 `proxy` 配置；国内直连 Pixiv 不可达。
- **图片发送失败**：多为网络抖动或代理带宽不足，稍后再试；回复里附有作品页链接可直接查看。
