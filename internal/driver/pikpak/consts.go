package pikpak

// 协议常量（非官方 API，按公开接口行为自实现；常量为互操作所需的事实数据）。
// 盐表/client_id 随 PikPak 客户端版本变化，失效时集中在此更新（2026-07 核对）。

const (
	defaultAuthBase  = "https://user.mypikpak.net"
	defaultDriveBase = "https://api-drive.mypikpak.net"
	redirectURI      = "xlaccsdk01://xbase.cloud/callback?state=harbor"
)

// platformConsts 是一组客户端身份常量；captcha 签名与其绑定。
type platformConsts struct {
	clientID      string
	clientSecret  string
	clientVersion string
	packageName   string
	userAgent     string
	salts         []string
}

var platforms = map[string]platformConsts{
	"android": {
		clientID:      "YNxT9w7GMdWvEOKa",
		clientSecret:  "dbw2OtmVEeuUvIptb1Coyg",
		clientVersion: "1.53.2",
		packageName:   "com.pikcloud.pikpak",
		userAgent: "ANDROID-com.pikcloud.pikpak/1.53.2 protocolVersion/200 accesstype/ " +
			"clientid/YNxT9w7GMdWvEOKa clientversion/1.53.2 action_type/ networktype/WIFI " +
			"sessionid/ providername/NONE sdkversion/2.0.6.206003 " +
			"devicename/Xiaomi_M2004j7ac osversion/13 platformversion/10 devicemodel/M2004J7AC",
		salts: []string{
			"SOP04dGzk0TNO7t7t9ekDbAmx+eq0OI1ovEx",
			"nVBjhYiND4hZ2NCGyV5beamIr7k6ifAsAbl",
			"Ddjpt5B/Cit6EDq2a6cXgxY9lkEIOw4yC1GDF28KrA",
			"VVCogcmSNIVvgV6U+AochorydiSymi68YVNGiz",
			"u5ujk5sM62gpJOsB/1Gu/zsfgfZO",
			"dXYIiBOAHZgzSruaQ2Nhrqc2im",
			"z5jUTBSIpBN9g4qSJGlidNAutX6",
			"KJE2oveZ34du/g1tiimm",
		},
	},
	"web": {
		clientID:      "YUMx5nI8ZU8Ap8pm",
		clientSecret:  "dbw2OtmVEeuUvIptb1Coyg",
		clientVersion: "2.0.0",
		packageName:   "mypikpak.com",
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36",
		salts: []string{
			"C9qPpZLN8ucRTaTiUMWYS9cQvWOE",
			"+r6CQVxjzJV6LCV",
			"F",
			"pFJRC",
			"9WXYIDGrwTCz2OiVlgZa90qpECPD6olt",
			"/750aCr4lm/Sly/c",
			"RB+DT/gZCrbV",
			"",
			"CyLsf7hdkIRxRm215hl",
			"7xHvLi2tOYP0Y92b",
			"ZGTXXxu8E/MIWaEDB+Sm/",
			"1UI3",
			"E7fP5Pfijd+7K+t6Tg/NhuLq0eEUVChpJSkrKxpO",
			"ihtqpG6FMt65+Xk+tWUH2",
			"NhXXU9rg4XXdzo7u5o",
		},
	},
	"pc": {
		clientID:      "YvtoWO6GNHiuCl7x",
		clientSecret:  "1NIH5R1IEe2pAxZE3hv3uA",
		clientVersion: "undefined",
		packageName:   "mypikpak.com",
		userAgent: "MainWindow Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) PikPak/2.6.11.4955 Chrome/100.0.4896.160 Electron/18.3.15 Safari/537.36",
		salts: []string{
			"KHBJ07an7ROXDoK7Db",
			"G6n399rSWkl7WcQmw5rpQInurc1DkLmLJqE",
			"JZD1A3M4x+jBFN62hkr7VDhkkZxb9g3rWqRZqFAAb",
			"fQnw/AmSlbbI91Ik15gpddGgyU7U",
			"/Dv9JdPYSj3sHiWjouR95NTQff",
			"yGx2zuTjbWENZqecNI+edrQgqmZKP",
			"ljrbSzdHLwbqcRn",
			"lSHAsqCkGDGxQqqwrVu",
			"TsWXI81fD1",
			"vk7hBjawK/rOSrSWajtbMk95nfgf3",
		},
	},
}
