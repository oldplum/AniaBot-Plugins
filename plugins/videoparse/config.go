package videoparse

// videoParseConfig 视频解析插件配置。实现 plugin.ConfigSchemaProvider 后，
// 面板「配置管理」自动渲染表单，框架在 Start 前把值填进 p.cfg。
type videoParseConfig struct {
	Enable bool `cfg:"plugin.videoparse.enable" label:"启用插件" group:"视频解析" default:"true" help:"关闭后不响应任何链接与命令"`

	AutoParse bool `cfg:"plugin.videoparse.auto_parse" label:"自动解析" group:"视频解析" default:"true" help:"开启后群里发分享链接即自动解析；关闭则仅响应 /解析 命令"`

	GroupIDs []string `cfg:"plugin.videoparse.group_ids" label:"生效群聊列表" type:"strings" group:"视频解析" help:"只在这些群自动解析（逗号分隔；QQ 填群号，其他平台带前缀如 tg:-100xxx）；留空 = 所有群生效"`

	APIURL string `cfg:"plugin.videoparse.api_url" label:"解析接口地址" group:"视频解析" default:"https://www.downcats.com/v1/extract/free/video" help:"DownCats（蓝猫素材助手）提取接口，支持抖音/快手/B站；填 off 关闭接口（抖音将不可用，B站与快手仍可本地解析）。免费版按 IP 有次数上限，用尽后需等重置或换成自建服务"`

	PreferLocal bool `cfg:"plugin.videoparse.prefer_local" label:"优先本地解析" group:"视频解析" default:"false" help:"B站/快手优先用官方接口与分享页解析，不消耗解析接口额度，把额度留给抖音；接口仅作失败兜底。群活跃时建议开启"`

	MaxSizeMB int `cfg:"plugin.videoparse.max_size_mb" label:"视频体积上限(MB)" group:"视频解析" default:"200" help:"超过该体积的视频不下载，避免刷屏与流量浪费；抖音高清原片常见 100MB 以上，可按需调整"`

	SendTitle bool `cfg:"plugin.videoparse.send_title" label:"附带标题信息" group:"视频解析" default:"true" help:"发送视频前附一行「标题 - 作者」文本"`
}
