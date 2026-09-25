package api

import "errors"

// 共用的 sentinel 錯誤，讓各支 handler 對同一種狀況回傳一致的錯誤訊息。
var (
	errNoPoolConfigured    = errors.New("no storage pool has been configured yet: PUT /api/v1/storage/pool first")
	errAppNotFound         = errors.New("no installed app with that id")
	errShareNotFound       = errors.New("no share with that name")
	errExportNotFound      = errors.New("no NFS export with that path")
	errExportPathRequired  = errors.New("the export path to delete is required")
	errShareAlreadyExists  = errors.New("a share with that name already exists")
	errExportAlreadyExists = errors.New("an NFS export for that path already exists")
	errUserNotFound        = errors.New("no user with that username")
	errPasswordRequired    = errors.New("password is required")
	errAlertRuleNotFound   = errors.New("no alert rule with that id")
	errNotifierNotFound    = errors.New("no notifier with that id")

	// Phase 14：email 通知管道共用的 sentinel 錯誤。
	errEmailNotifierNotFound = errors.New("no email notifier with that id")

	// Phase 6：身分驗證、HTTPS、WireGuard 共用的 sentinel 錯誤。
	errNotAuthenticated       = errors.New("not authenticated: please log in")
	errAdminAlreadyConfigured = errors.New("an admin account already exists")
	errUsernameRequired       = errors.New("username is required")
	errPasswordTooShort       = errors.New("password must be at least 8 characters")
	errAdminNotConfigured     = errors.New("no admin account has been configured yet: complete first-run setup first")
	errInvalidCredentials     = errors.New("invalid username or password")
	errInvalidTOTPCode        = errors.New("invalid or expired two-factor authentication code")
	errTOTPNotSetUp           = errors.New("two-factor authentication has not been set up yet")
	errVPNNotConfigured       = errors.New("no wireguard interface has been configured yet: PUT /api/v1/vpn/interface first")
	errPeerNotFound           = errors.New("no wireguard peer with that id")
	errPeerNameExists         = errors.New("a wireguard peer with that name already exists")
	errPeerNameRequired       = errors.New("peer name is required")
	errPeerAllowedIPsEmpty    = errors.New("at least one allowed IP/CIDR is required")

	errBackupJobNotFound = errors.New("no backup job with that id")

	// Phase 9.1：自訂 App 安裝(不透過內建目錄範本)共用的 sentinel 錯誤。
	errAppInstallNeedsExactlyOne = errors.New("provide exactly one of templateId or template")
	errAppIDConflictsWithCatalog = errors.New("this id is already used by a built-in catalog template")
	errAppIDAlreadyInstalled     = errors.New("an app with this id is already installed")

	// Phase 9：登入節流、panic 復原共用的 sentinel 錯誤。
	errTooManyLoginAttempts = errors.New("too many failed login attempts, please try again later")
	errInternalServerError  = errors.New("internal server error")

	// 第五十八輪資安覆核(#6):跨站(非同源)的狀態變更請求被擋下時回這個。
	errCrossOriginBlocked = errors.New("cross-origin request blocked: this request did not come from the GoNAS web interface")

	// 第五十八輪:SnapRAID 同位同步(handleStorageArraySync)共用的 sentinel 錯誤。
	errNoParityDisks        = errors.New("this pool has no parity disk, so there is no parity to sync: add a parity disk in the pool settings first")
	errParitySyncInProgress = errors.New("a parity sync is already running, please wait for it to finish")

	// Phase 10：檔案管理員共用的 sentinel 錯誤。
	errArrayNotStarted = errors.New("the storage array has been configured but is not started yet: POST /api/v1/storage/array/start first")
	errMissingPath     = errors.New("the \"path\" query parameter is required")
	errMissingFromOrTo = errors.New("both \"from\" and \"to\" are required")
	errNoUploadedFile  = errors.New("no file was found in the upload request")

	// Phase 13：多管理帳號/角色權限共用的 sentinel 錯誤。
	errInsufficientPermission = errors.New("this account does not have permission to perform this action: an admin-role account is required")
	errInvalidRole            = errors.New("role must be either \"admin\" or \"viewer\"")
	errAdminUsernameTaken     = errors.New("an account with that username already exists")
	errAdminAccountNotFound   = errors.New("no account with that username")
	errCannotDeleteOwnAccount = errors.New("you cannot delete your own account while logged in as it: log in as a different admin account first")
	errCannotResetOwnTOTP     = errors.New("to turn off your own two-factor authentication, use the disable option (which asks for your password)")
	errCannotDeleteLastAdmin  = errors.New("cannot delete the last remaining admin-role account: at least one must always exist")

	// 第十九輪:預設 admin(gonas/gonas)第一次登入必須先改密碼,改掉
	// 之前 requireAdmin 會用這個錯誤擋掉所有 admin 操作。前端主要靠
	// /auth/me 回傳的 mustChangePassword 旗標來強制導向改密碼畫面,這個
	// 403 是「就算有人繞過前端直接打 API 也擋得住」的伺服器端硬性保險。
	errPasswordChangeRequired = errors.New("you must change the default password before performing this action")

	// Phase 17：自我更新(internal/api/system_update_handlers.go)共用的
	// sentinel 錯誤。跟這個檔案裡其他錯誤一樣固定用英文——前端
	// i18n.js 的 errorMap 負責翻成使用者介面語言,見該檔案開頭的說明。
	errUpdateNotConfigured     = errors.New("no update manifest url has been configured yet: set one first")
	errUpdateAlreadyInProgress = errors.New("an update is already being downloaded and applied, please wait")

	// Phase 18a：自我更新一鍵復原(handleSystemUpdateRollback)專用的
	// sentinel 錯誤。
	errUpdateNoBackupAvailable = errors.New("no previous version backup is available to roll back to")
)
