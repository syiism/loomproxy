package db

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"loomproxy/models"
)

// 系统设置读取缓存：避免热路径（如 base.Fetch 的按数据源代理判定）每请求查库。
// 管理后台修改设置后最长 10s 生效，可接受。
type settingCacheEntry struct {
	value string
	exp   time.Time
}

var settingCache sync.Map // key -> settingCacheEntry

const settingCacheTTL = 10 * time.Second

// GetSetting 读取系统设置（带 10s 内存缓存，含空值负缓存）；不存在或出错返回 ""
func GetSetting(key string) string {
	if v, ok := settingCache.Load(key); ok {
		if e := v.(settingCacheEntry); time.Now().Before(e.exp) {
			return e.value
		}
	}
	value := ""
	if DB != nil {
		var s models.SystemSetting
		// 条件用 map 形式让 GORM 按方言加引号：`key` 在 MySQL 是保留字，而反引号写死会让
		// PostgreSQL 直接语法报错、错误被吞掉后所有设置都读成空串（见待办清单 P1）
		if err := DB.Where(map[string]interface{}{"key": key}).First(&s).Error; err == nil {
			value = s.Value
		}
	}
	settingCache.Store(key, settingCacheEntry{value: value, exp: time.Now().Add(settingCacheTTL)})
	return value
}

// InvalidateSettingCache 使设置缓存失效：管理后台修改后调用，立即生效
// （不再等 10s TTL）；key 为空时清空全部
func InvalidateSettingCache(key string) {
	if key == "" {
		settingCache.Range(func(k, _ interface{}) bool {
			settingCache.Delete(k)
			return true
		})
		return
	}
	settingCache.Delete(key)
}

// SettingBool 读一个 `type=bool` 的设置项。
//
// **为什么要有它**：同一族判据原来有四份写法——`getSettingBool` 去空白转小写、
// `deviceWatchEnabled` / `auto_block_enabled` / 「设备与密钥」读数三处是**精确 `== "true"`**。
// 于是库里存 `True`（人或脚本敲的）时，`register_enabled` 读成"开"而另一些读成"关"：
// 同一个值在两个开关上给出相反结果，而写入端那时只校验 `json` 型（待办清单 P60）。
// 空串=这一行不存在或值为空，按 `def`；其余一律"去空白 + 小写后是否等于 true"。
func SettingBool(key string, def bool) bool {
	raw := strings.TrimSpace(GetSetting(key))
	if raw == "" {
		return def
	}
	return strings.EqualFold(raw, "true")
}

// SettingInt 读一个 `type=number` 的设置项，并在值没生效时出声。
//
// **为什么要有它**：同一个形状原来有三份写法，而且三份的失败面不一样——
// `handlers/auth` 那份（P48 修的）出声；`handlers/verify` 那份是手写的逐字符十进制解析，
// 任何非数字字符（含粘贴进来的空格、负号）与 0 都**静默**回默认，还会在大数上溢出回默认；
// `middleware/ipblock` 那份是 `strconv.Atoi` 配 `, _`，同样静默。
// 于是同一个填错的值，在三个开关上是三种不同的"悄悄不按你填的走"（待办清单 P61）。
//
// 语义与三份一致（**这是收口**）：空=按默认，非整数=按默认，非正数=按默认；
// 三种都出一条 ERROR，同一种替换只喊一次。刻意留下的两处行为差异都朝好的方向：
// 带首尾空白的合法数字以前算"没填对"（同 P48 的 TrimSpace），超长数字串以前在手工解析里溢出成
// 一个没人认识的数、现在按默认走并出声。0 到底该不该当"没配"是 P48② 那件还没拍的事，
// 拍的时候只改这一处。合法域的上界仍由调用方管（各键对取值域的理解不同，见各包自己的判法）。
func SettingInt(key string, def int) int {
	return settingIntFloor(key, def, 1, "填了非正数")
}

// SettingIntNonNeg 与 `SettingInt` 唯一的差别：**0 是合法值**，按 0 走且不出声。
//
// 存在的理由是"0 有业务含义"的键确实存在，不是假想：携带形态里 uxx 的广告冷却标定用 0 当
// **哨兵**（库里那一行是 0 表示"还没有边界证据"，判据写的就是 `hi == 0`），
// 冷却本身也可以被管理员设成 0，意思是"不设冷却、立刻可试"。
// 拿 `SettingInt` 读它们的症状是"我明明填了 0，它却每次等三小时"（分支侧 S50）。
//
// **别把它当"想允许 0 就走这条"的万能口**：P48② 那一问（上限类设置里的 0 是什么意思）还没拍，
// 拍完之后这两条的分界应当只剩一处说法。负数两种都不收——它不是任何一种已定义的口径。
func SettingIntNonNeg(key string, def int) int {
	return settingIntFloor(key, def, 0, "填了负数")
}

// settingIntFloor 是上面两条的共同实现：floor 是**含端点的合法下界**，越界时用它给的措辞出声。
// 写成一个实现而不是两份并排，是因为这一整轮的病因就是"同一个判据抄了三遍、每遍漏掉不同的面"。
func settingIntFloor(key string, def int, floor int, reason string) int {
	raw := GetSetting(key)
	trimmed := strings.TrimSpace(raw) // 粘贴进来的值常带首尾空白，"看着填对了、实际按默认走"也是这条要治的病
	n, err := strconv.Atoi(trimmed)
	switch {
	case trimmed == "":
		NoticeReplacedSetting(key, raw, strconv.Itoa(def), "没填或设置行不存在")
	case err != nil:
		NoticeReplacedSetting(key, raw, strconv.Itoa(def), "不是整数")
	case n < floor:
		NoticeReplacedSetting(key, raw, strconv.Itoa(def), reason)
	default:
		return n
	}
	return def
}

// settingNotices 记录每个设置键上一次出声时的样子（原因 + 填的值），同一种替换只说一次。
// 键的个数就是设置项里被这么读的键的个数，不回收。
var settingNotices sync.Map // key -> string

// NoticeReplacedSetting 在"配置里填的值没生效、系统按兜底值走"时说一次话。
//
// 合法域的判断留在调用方（各键对取值域的理解不同），这里只管"要出声"这一件事；
// 生效值一律按字符串传（数字键自己 `Itoa`），否则字符串型的设置就没有办法用同一个出口说话。
// 去重（同 P36 号池 `ErrCapacityReached` 的判据）是必需的：读配置在热路径上，
// 每请求一条日志会把真正该看的读数泡坏。
func NoticeReplacedSetting(key, raw string, effective string, reason string) {
	sig := reason + "\x00" + raw
	if v, ok := settingNotices.Load(key); ok && v.(string) == sig {
		return
	}
	settingNotices.Store(key, sig)
	log.Printf("ERROR: 设置 %s 填的值没生效（%s）：填的是 %q，实际按 %q 走", key, reason, raw, effective)
}

// SensitiveSettingKeys 只写不回显的设置键（待办清单 P62）：值里躺着对外通道的真实令牌，
// 面板与管理 API 一律不回显（读响应里是空串）；写入端留空 = 保持原值不变（防「看一眼再保存」把掩码存回去）。
// 新增敏感键进这张表，面板经 GET /admin/settings 的 sensitive_keys 拿到名单渲染密钥控件。
var SensitiveSettingKeys = map[string]bool{
	"verify_http_headers": true, // 对接发码通道的请求头，内含真实访问令牌
}
