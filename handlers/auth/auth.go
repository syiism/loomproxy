package auth

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/handlers/verify"
	"loomproxy/models"
	"loomproxy/utils"
)

type registerRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8,max=16"`
	Nickname string `json:"nickname"`
	// VerificationCode 注册场景启用验证码时必填（verify_code_scenes 含 register）
	VerificationCode string `json:"verification_code"`
}

type loginRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password" binding:"required"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8,max=16"`
}

type forgotPasswordRequest struct {
	Username        string `json:"username" binding:"required"`
	Email           string `json:"email" binding:"required,email"`
	NewPassword     string `json:"new_password" binding:"required"`
	ConfirmPassword string `json:"confirm_password" binding:"required"`
	// VerificationCode 找回密码场景启用验证码时必填（verify_code_scenes 含 forgot_password）
	VerificationCode string `json:"verification_code"`
}

func ok(c *gin.Context, data interface{}) {
	Ok(c, data)
}

func fail(c *gin.Context, status int, msg string) {
	Fail(c, status, msg)
}

// Ok 公开响应辅助，供 admin 包复用
func Ok(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "ok",
		"data": data,
	})
}

// Fail 公开错误响应辅助，供 admin 包复用
func Fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{
		"code": conf.Config.ErrorCode,
		"msg":  msg,
	})
}

func RegisterRoutes(r *gin.Engine) {
	auth := r.Group("/auth")
	{
		auth.POST("/register", Register)
		auth.POST("/login", Login)
		auth.POST("/forgot-password", ForgotPassword)
		auth.GET("/me", AuthRequired(), Me)
		auth.PATCH("/me", AuthRequired(), UpdateMe)
		auth.POST("/password", AuthRequired(), ChangePassword)
		auth.POST("/logout", AuthRequired(), Logout)
		auth.GET("/sessions", AuthRequired(), ListSessions)
		auth.POST("/sessions/revoke-others", AuthRequired(), RevokeOtherSessions)
		auth.DELETE("/sessions/:id", AuthRequired(), RevokeSession)
	}
}

func getSettingBool(key string, def bool) bool {
	var setting models.SystemSetting
	if err := db.DB.Where("`key` = ?", key).First(&setting).Error; err != nil {
		return def
	}
	return strings.ToLower(setting.Value) == "true"
}

func getSettingStr(key string, def string) string {
	var setting models.SystemSetting
	if err := db.DB.Where("`key` = ?", key).First(&setting).Error; err != nil {
		return def
	}
	return setting.Value
}

func loadUserRoles(user *models.User) {
	db.DB.Model(user).Association("Roles").Find(&user.Roles)
}

// registerBindError 将注册请求的绑定/校验错误翻译为友好的中文提示，
// 避免把 gin validator 的英文原始错误（Key: 'xxx' Error:...）直接抛给前端
func registerBindError(err error) string {
	var ve validator.ValidationErrors
	if errors.As(err, &ve) {
		for _, fe := range ve {
			switch fe.Field() {
			case "Username":
				if fe.Tag() == "required" {
					return "用户名不能为空"
				}
				return "用户名长度需为 3-64 个字符"
			case "Email":
				if fe.Tag() == "required" {
					return "邮箱不能为空"
				}
				return "邮箱格式不正确"
			case "Password":
				if fe.Tag() == "required" {
					return "密码不能为空"
				}
				return "密码长度需为 8-16 位"
			}
		}
	}
	return "请求参数格式不正确"
}

// passwordError 返回密码不满足强度要求的具体原因，满足时返回空串
func passwordError(pw string) string {
	if len(pw) < 8 || len(pw) > 16 {
		return "密码长度需为 8-16 位"
	}
	if !hasLetter.MatchString(pw) {
		return "密码需包含至少一个字母"
	}
	if !hasDigit.MatchString(pw) {
		return "密码需包含至少一个数字"
	}
	return ""
}

func Register(c *gin.Context) {
	if !getSettingBool("register_enabled", true) {
		fail(c, http.StatusForbidden, "注册功能已关闭")
		return
	}

	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, registerBindError(err))
		return
	}

	// 注册场景启用验证码时先校验（Check 不消费，注册成功后才 Consume，中途失败不烧码）
	var vcRow *models.VerificationCode
	if verify.SceneEnabled(verify.SceneRegister) {
		row, err := verify.Check(verify.SceneRegister, req.Email, req.VerificationCode)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		vcRow = row
	}

	if msg := passwordError(req.Password); msg != "" {
		fail(c, http.StatusBadRequest, msg)
		return
	}

	username := strings.TrimSpace(strings.ToLower(req.Username))
	email := strings.TrimSpace(strings.ToLower(req.Email))

	var existing models.User
	// 用 Unscoped 查重：软删除用户的用户名/邮箱进入黑名单，不可再注册
	err := db.DB.Unscoped().Where("username = ?", username).First(&existing).Error
	if err == nil {
		if existing.DeletedAt.Valid {
			fail(c, http.StatusConflict, "该用户名已被注销账号占用，不可注册")
		} else {
			fail(c, http.StatusConflict, "用户名已存在")
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	err = db.DB.Unscoped().Where("email = ?", email).First(&existing).Error
	if err == nil {
		if existing.DeletedAt.Valid {
			fail(c, http.StatusConflict, "该邮箱已被注销账号占用，不可注册")
		} else {
			fail(c, http.StatusConflict, "邮箱已注册")
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	defaultRoleCode := getSettingStr("default_role", "user")
	var defaultRole models.Role
	if err := db.DB.Where("code = ?", defaultRoleCode).First(&defaultRole).Error; err != nil {
		db.DB.Where("code = ?", "user").First(&defaultRole)
	}

	var defaultPlanID *uint
	var freePlan models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&freePlan).Error; err == nil {
		defaultPlanID = &freePlan.ID
	}

	user := &models.User{
		Username: username,
		Email:    email,
		Nickname: req.Nickname,
		Status:   1,
		PlanID:   defaultPlanID,
		Roles:    []models.Role{defaultRole},
	}
	if err := user.SetPassword(req.Password); err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}

	if err := db.DB.Create(user).Error; err != nil {
		fail(c, http.StatusInternalServerError, "注册失败: "+err.Error())
		return
	}
	if vcRow != nil && !verify.Consume(vcRow.ID) {
		// 并发下验证码被占用（理论极小概率）：注册已成功，仅记日志
		log.Printf("WARN: 注册验证码消费失败（可能并发复用）row_id=%d user_id=%d", vcRow.ID, user.ID)
	}

	now := time.Now()
	user.LastLoginAt = &now
	if err := db.DB.Model(user).Update("last_login_at", now).Error; err != nil {
		// 更新最后登录时间失败不阻断注册
	}

	loadUserRoles(user)

	token, expiresAt, err := issueToken(c, user.ID, user)
	if err != nil {
		fail(c, http.StatusInternalServerError, "生成 token 失败")
		return
	}

	ok(c, gin.H{
		"token":      token,
		"expires_at": expiresAtJSON(expiresAt),
		"user":       user.Public(),
	})
}

// resolveTokenExpireHours 登录 token 有效时长（小时）解析优先级：
// 用户个人设置（token_expire_hours）> 系统设置（jwt_expire_hours）> 环境变量 JWT_EXPIRE_HOURS > 默认 168h；
// 任一级为 -1 时永不过期（不再向下回退）
func resolveTokenExpireHours(user *models.User) int {
	if user.TokenExpireHours != nil {
		if *user.TokenExpireHours == -1 {
			return -1
		}
		if *user.TokenExpireHours > 0 {
			return *user.TokenExpireHours
		}
	}
	if v := getSettingStr("jwt_expire_hours", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n == -1 {
				return -1
			}
			if n > 0 {
				return n
			}
		}
	}
	if conf.Config.JWTExpireHours == -1 {
		return -1
	}
	if conf.Config.JWTExpireHours > 0 {
		return conf.Config.JWTExpireHours
	}
	return 24 * 7
}

// expiresAtJSON 登录响应的 expires_at 字段：零值（永不过期）返回 nil
func expiresAtJSON(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

// issueToken 创建登录会话并签发带 jti 的 JWT，同时写入 Cookie
func issueToken(c *gin.Context, userID uint, user *models.User) (string, time.Time, error) {
	sessionID, err := db.NewSessionID()
	if err != nil {
		return "", time.Time{}, err
	}
	expireHours := resolveTokenExpireHours(user)
	token, expiresAt, err := utils.GenerateToken(user, sessionID, expireHours)
	if err != nil {
		return "", time.Time{}, err
	}

	// 会话记录是必需的（ValidateSession 依赖），失败则阻断。
	// 永不过期（expireHours=-1）时会话表 ExpiresAt 非空，写 100 年表达“永不”
	sessionExpiresAt := expiresAt
	if expireHours == -1 {
		sessionExpiresAt = time.Now().AddDate(100, 0, 0)
	}
	ua := c.GetHeader("User-Agent")
	if err := db.CreateSession(&models.AuthSession{
		SessionID:    sessionID,
		UserID:       userID,
		Device:       utils.ParseDevice(ua),
		UserAgent:    ua,
		IP:           c.ClientIP(),
		LastActiveAt: time.Now(),
		ExpiresAt:    sessionExpiresAt,
	}); err != nil {
		return "", time.Time{}, err
	}
	utils.SetTokenCookie(c, token, expireHours)
	return token, expiresAt, nil
}

func Login(c *gin.Context) {
	// 登录是公开接口中最常被爆破的入口：与找回密码同口径的按 IP 限频。
	// 用户不存在与密码错误统一返回「用户名或密码错误」并计入失败；
	// 账号禁用（403）不属凭证探测，不计数
	ip := c.ClientIP()
	if until, limited := loginLimiter.locked(ip); limited {
		fail(c, http.StatusTooManyRequests, "尝试过于频繁，请 "+until.Format("15:04")+" 后再试")
		return
	}

	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	username := strings.TrimSpace(strings.ToLower(req.Username))
	email := strings.TrimSpace(strings.ToLower(req.Email))

	var user models.User
	var err error

	if username != "" {
		// 前端登录框为「用户名 / 邮箱」单输入框，统一填在 username 字段提交，
		// 因此此处同时按用户名和邮箱匹配（两者均有唯一索引，不会歧义）
		err = db.DB.Where("username = ? OR email = ?", username, username).First(&user).Error
	} else if email != "" {
		err = db.DB.Where("email = ?", email).First(&user).Error
	} else {
		fail(c, http.StatusBadRequest, "请输入用户名或邮箱")
		return
	}

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			loginLimiter.record(ip, false)
			fail(c, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	if user.Status != 1 {
		fail(c, http.StatusForbidden, "账号已被禁用")
		return
	}

	if !user.CheckPassword(req.Password) {
		loginLimiter.record(ip, false)
		fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	// 更新最后登录时间，失败不阻断登录
	now := time.Now()
	if err := db.DB.Model(&user).Update("last_login_at", now).Error; err == nil {
		user.LastLoginAt = &now
	}

	loadUserRoles(&user)

	token, expiresAt, err := issueToken(c, user.ID, &user)
	if err != nil {
		fail(c, http.StatusInternalServerError, "生成 token 失败")
		return
	}

	loginLimiter.record(ip, true)
	ok(c, gin.H{
		"token":      token,
		"expires_at": expiresAtJSON(expiresAt),
		"user":       user.Public(),
	})
}

func Me(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var user models.User
	if err := db.DB.Preload("Roles").Preload("Plan").First(&user, userID).Error; err != nil {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	ok(c, user.Public())
}

// usernameChangeCooldown 用户名修改冷却期：每 30 天限改一次
const usernameChangeCooldown = 30 * 24 * time.Hour

// tokenExpireHoursMax 个人登录时长上限（小时），约 1 年
const tokenExpireHoursMax = 8760

type updateMeRequest struct {
	Username         string `json:"username"`
	Nickname         string `json:"nickname"`
	Email            string `json:"email" binding:"required,email"`
	TokenExpireHours *int   `json:"token_expire_hours"` // 0=清除个人设置（跟随系统），1-8760=个人时长
}

// UpdateMe 修改当前用户资料（用户名 / 昵称 / 邮箱）
// 用户名每 30 天限改一次（从未修改过不受限），且不可与其他用户重复
func UpdateMe(c *gin.Context) {
	var req updateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	userID, _ := c.Get("user_id")

	var user models.User
	if err := db.DB.First(&user, userID).Error; err != nil {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	updates := map[string]interface{}{}
	usernameChanged := false
	if req.Nickname != "" {
		updates["nickname"] = strings.TrimSpace(req.Nickname)
	}
	if req.Username != "" {
		newUsername := strings.ToLower(strings.TrimSpace(req.Username))
		if newUsername != user.Username {
			if len(newUsername) < 3 || len(newUsername) > 64 {
				fail(c, http.StatusBadRequest, "用户名长度需为 3-64 个字符")
				return
			}
			// 30 天冷却期（从未修改过不受限）
			if user.UsernameChangedAt != nil {
				next := user.UsernameChangedAt.Add(usernameChangeCooldown)
				if time.Now().Before(next) {
					fail(c, http.StatusBadRequest, "用户名每 30 天只能修改一次，下次可修改时间："+next.Format("2006-01-02 15:04"))
					return
				}
			}
			var existing models.User
			err := db.DB.Where("username = ? AND id <> ?", newUsername, user.ID).First(&existing).Error
			if err == nil {
				fail(c, http.StatusConflict, "用户名已存在")
				return
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				fail(c, http.StatusInternalServerError, "数据库错误")
				return
			}
			updates["username"] = newUsername
			updates["username_changed_at"] = time.Now()
			usernameChanged = true
		}
	}
	cleaned := strings.ToLower(strings.TrimSpace(req.Email))
	if cleaned == "" {
		fail(c, http.StatusBadRequest, "邮箱不能为空")
		return
	}
	var err error
	var existing models.User
	err = db.DB.Where("email = ? AND id <> ?", cleaned, user.ID).First(&existing).Error
	if err == nil {
		fail(c, http.StatusConflict, "邮箱已被使用")
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	updates["email"] = cleaned

	// 个人登录 token 有效时长：0 清除（跟随系统），-1 永不过期，否则 1-8760 小时，对下次及以后登录生效
	if req.TokenExpireHours != nil {
		if *req.TokenExpireHours < -1 || *req.TokenExpireHours > tokenExpireHoursMax {
			fail(c, http.StatusBadRequest, "登录有效时长需为 -1（永不过期）或 0-8760 小时（0=跟随系统默认）")
			return
		}
		if *req.TokenExpireHours == 0 {
			updates["token_expire_hours"] = nil
		} else {
			updates["token_expire_hours"] = *req.TokenExpireHours
		}
	}

	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "无更新字段")
		return
	}

	if err := db.DB.Model(&user).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新失败")
		return
	}

	// 用户名即登录凭证：变更后吊销该用户全部会话（含当前），强制使用新用户名重新登录
	if usernameChanged {
		db.RevokeOtherSessions(user.ID, "")
	}

	// 重新加载最新数据（含角色）后返回
	if err := db.DB.Preload("Roles").First(&user, user.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	ok(c, user.Public())
}

func ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	userID, _ := c.Get("user_id")

	var user models.User
	if err := db.DB.First(&user, userID).Error; err != nil {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	if !user.CheckPassword(req.OldPassword) {
		fail(c, http.StatusUnauthorized, "原密码错误")
		return
	}

	if !validatePassword(req.NewPassword) {
		fail(c, http.StatusBadRequest, "密码必须为 8-16 位且包含字母和数字")
		return
	}

	if err := user.SetPassword(req.NewPassword); err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}

	if err := db.DB.Save(&user).Error; err != nil {
		fail(c, http.StatusInternalServerError, "修改密码失败")
		return
	}

	ok(c, gin.H{"message": "密码修改成功"})
}

// 公开接口的按 IP 尝试限频（内存计数，重启清零），防账号枚举与暴力探测。
// 登录与找回密码各持一个实例：找回是低频操作，5 次/分钟足够；
// 登录放宽到 10 次/分钟，避免 NAT 出口 IP 下多个正常用户同时重登录被误锁
const (
	forgotMaxPerMinute = 5
	loginMaxPerMinute  = 10
	attemptMaxFails    = 10
	attemptLockMinutes = time.Hour
)

// ipAttemptLimiter 按客户端 IP 计数的尝试限频器（登录、找回密码等公开接口复用）
type ipAttemptLimiter struct {
	mu           sync.Mutex
	attempts     map[string]*ipAttempt
	maxPerMinute int
}

type ipAttempt struct {
	windowStart time.Time
	count       int
	failStreak  int
	lockedUntil time.Time
}

func newIPAttemptLimiter(maxPerMinute int) *ipAttemptLimiter {
	return &ipAttemptLimiter{attempts: make(map[string]*ipAttempt), maxPerMinute: maxPerMinute}
}

// locked 返回锁定截止时间与是否被限制
func (l *ipAttemptLimiter) locked(ip string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[ip]
	if a == nil {
		return time.Time{}, false
	}
	if time.Now().Before(a.lockedUntil) {
		return a.lockedUntil, true
	}
	return time.Time{}, false
}

func (l *ipAttemptLimiter) record(ip string, success bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[ip]
	if a == nil {
		a = &ipAttempt{windowStart: time.Now()}
		l.attempts[ip] = a
	}
	now := time.Now()
	if now.Sub(a.windowStart) > time.Minute {
		a.windowStart = now
		a.count = 0
	}
	a.count++
	if a.count > l.maxPerMinute {
		a.lockedUntil = now.Add(attemptLockMinutes)
	}
	if success {
		a.failStreak = 0
	} else {
		a.failStreak++
		if a.failStreak >= attemptMaxFails {
			a.lockedUntil = now.Add(attemptLockMinutes)
		}
	}
}

var (
	forgotLimiter = newIPAttemptLimiter(forgotMaxPerMinute)
	loginLimiter  = newIPAttemptLimiter(loginMaxPerMinute)
)

// ResetAttemptLimitersForTest 清空登录/找回密码的限频状态。
// 限频器为进程级状态且按 IP 计数，集成测试所有用例共享 127.0.0.1，
// 每个新测试服务创建时调用以隔离用例（与 verify.MockLastCode 同为测试钩子，生产代码不应调用）
func ResetAttemptLimitersForTest() {
	for _, l := range []*ipAttemptLimiter{forgotLimiter, loginLimiter} {
		l.mu.Lock()
		l.attempts = make(map[string]*ipAttempt)
		l.mu.Unlock()
	}
}

// ForgotPassword 找回密码：校验「用户名 + 邮箱」匹配后重置为新密码，并自动登录
// （吊销全部旧会话后签发新 token）。用户不存在与邮箱不匹配返回统一的模糊错误，防账号枚举
func ForgotPassword(c *gin.Context) {
	ip := c.ClientIP()
	if until, limited := forgotLimiter.locked(ip); limited {
		fail(c, http.StatusTooManyRequests, "尝试过于频繁，请 "+until.Format("15:04")+" 后再试")
		return
	}

	var req forgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "用户名、邮箱、新密码、确认新密码均为必填项")
		return
	}
	if req.NewPassword != req.ConfirmPassword {
		fail(c, http.StatusBadRequest, "两次输入的新密码不一致")
		return
	}
	if msg := passwordError(req.NewPassword); msg != "" {
		fail(c, http.StatusBadRequest, msg)
		return
	}

	username := strings.TrimSpace(strings.ToLower(req.Username))
	email := strings.TrimSpace(strings.ToLower(req.Email))

	var user models.User
	err := db.DB.Where("username = ?", username).First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			forgotLimiter.record(ip, false)
			fail(c, http.StatusBadRequest, "用户名与邮箱不匹配")
			return
		}
		fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if user.Email != email {
		forgotLimiter.record(ip, false)
		fail(c, http.StatusBadRequest, "用户名与邮箱不匹配")
		return
	}
	if user.Status != 1 {
		fail(c, http.StatusForbidden, "账号已被禁用")
		return
	}

	// 找回密码场景启用验证码时校验（放在账号匹配之后：账号不存在/邮箱不符仍返回统一的
	// 「用户名与邮箱不匹配」，验证码不会被无谓消耗；Check 不消费，重置成功后才 Consume）
	var vcRow *models.VerificationCode
	if verify.SceneEnabled(verify.SceneForgotPassword) {
		row, err := verify.Check(verify.SceneForgotPassword, email, req.VerificationCode)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		vcRow = row
	}

	if err := user.SetPassword(req.NewPassword); err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := db.DB.Model(&user).Update("password_hash", user.PasswordHash).Error; err != nil {
		fail(c, http.StatusInternalServerError, "重置密码失败")
		return
	}
	if vcRow != nil {
		verify.Consume(vcRow.ID)
	}

	// 旧密码可能已泄露：吊销该用户全部旧会话，仅保留本次找回自动登录签发的会话
	db.RevokeOtherSessions(user.ID, "")

	// 更新最后登录时间，失败不阻断
	now := time.Now()
	if err := db.DB.Model(&user).Update("last_login_at", now).Error; err == nil {
		user.LastLoginAt = &now
	}

	loadUserRoles(&user)

	token, expiresAt, err := issueToken(c, user.ID, &user)
	if err != nil {
		fail(c, http.StatusInternalServerError, "生成 token 失败")
		return
	}

	forgotLimiter.record(ip, true)
	ok(c, gin.H{
		"token":      token,
		"expires_at": expiresAtJSON(expiresAt),
		"user":       user.Public(),
	})
}

func Logout(c *gin.Context) {
	if sessionID, ok := c.Get("session_id"); ok {
		if sid, ok := sessionID.(string); ok {
			db.RevokeSession(sid)
		}
	}
	utils.ClearTokenCookie(c)
	ok(c, gin.H{"message": "已退出登录"})
}

// ListSessions 当前用户的登录设备列表
func ListSessions(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	currentSID, _ := c.Get("session_id")

	sessions := db.ListActiveSessions(uid)
	list := make([]gin.H, 0, len(sessions))
	for _, s := range sessions {
		list = append(list, gin.H{
			"id":             s.ID,
			"device":         s.Device,
			"ip":             s.IP,
			"current":        s.SessionID == currentSID,
			"last_active_at": s.LastActiveAt,
			"created_at":     s.CreatedAt,
			"expires_at":     s.ExpiresAt,
		})
	}
	ok(c, list)
}

// RevokeOtherSessions 退出除当前设备外的全部登录
func RevokeOtherSessions(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	sessionID, _ := c.Get("session_id")
	sid, _ := sessionID.(string)

	n := db.RevokeOtherSessions(uid, sid)
	ok(c, gin.H{"message": "已退出其他设备", "count": n})
}

// RevokeSession 退出指定登录设备（仅限本人会话）
func RevokeSession(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		fail(c, http.StatusBadRequest, "参数错误")
		return
	}
	if !db.RevokeSessionByID(uid, uint(id)) {
		fail(c, http.StatusNotFound, "会话不存在或已退出")
		return
	}
	ok(c, gin.H{"message": "已退出该设备"})
}

func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, tokenStr, err := parseTokenFromContext(c)
		if err != nil {
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("token", tokenStr)
		c.Next()
	}
}

// AdminRequired 要求当前用户已登录且具有 admin 角色
func AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, _, err := parseTokenFromContext(c)
		if err != nil {
			c.Abort()
			return
		}

		var user models.User
		if err := db.DB.First(&user, claims.UserID).Error; err != nil {
			Fail(c, http.StatusUnauthorized, "用户不存在")
			c.Abort()
			return
		}
		loadUserRoles(&user)

		if !user.IsAdmin() {
			Fail(c, http.StatusForbidden, "需要管理员权限")
			c.Abort()
			return
		}

		c.Set("user_id", user.ID)
		c.Set("username", user.Username)
		c.Set("current_user", &user)
		c.Next()
	}
}

// parseTokenFromContext 从请求中解析 JWT 并处理错误响应
func parseTokenFromContext(c *gin.Context) (*utils.JWTClaims, string, error) {
	tokenStr := utils.TokenFromRequest(c)

	if tokenStr == "" {
		fail(c, http.StatusUnauthorized, "缺少登录凭证")
		return nil, "", errors.New("missing token")
	}

	claims, err := utils.ParseToken(tokenStr)
	if err != nil {
		if errors.Is(err, utils.ErrTokenExpired) {
			fail(c, http.StatusUnauthorized, "登录已过期，请重新登录")
		} else {
			fail(c, http.StatusUnauthorized, "无效的登录凭证")
		}
		return nil, "", err
	}

	// 会话校验：无 jti 的旧 token 或已吊销/过期的会话一律拒绝
	if !db.ValidateSession(claims.ID, claims.UserID) {
		fail(c, http.StatusUnauthorized, "登录已失效，请重新登录")
		return nil, "", errors.New("session invalid")
	}
	c.Set("session_id", claims.ID)

	return claims, tokenStr, nil
}

// ValidatePassword 检查密码强度：8-16 位且同时包含字母和数字
var (
	hasLetter = regexp.MustCompile(`[a-zA-Z]`)
	hasDigit  = regexp.MustCompile(`[0-9]`)
)

func ValidatePassword(pw string) bool {
	return len(pw) >= 8 && len(pw) <= 16 && hasLetter.MatchString(pw) && hasDigit.MatchString(pw)
}

func validatePassword(pw string) bool {
	return ValidatePassword(pw)
}
