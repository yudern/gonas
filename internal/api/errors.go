package api

import "errors"

// 共用的 sentinel 錯誤，讓各支 handler 對同一種狀況回傳一致的錯誤訊息。
var (
	errNoPoolConfigured   = errors.New("no storage pool has been configured yet: PUT /api/v1/storage/pool first")
	errAppNotFound        = errors.New("no installed app with that id")
	errShareNotFound      = errors.New("no share with that name")
	errShareAlreadyExists = errors.New("a share with that name already exists")
	errUserNotFound       = errors.New("no user with that username")
	errPasswordRequired   = errors.New("password is required")
	errAlertRuleNotFound  = errors.New("no alert rule with that id")
	errNotifierNotFound   = errors.New("no notifier with that id")

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
	errCannotDeleteLastAdmin  = errors.New("cannot delete the last remaining admin-role account: at least one must always exist")
)
