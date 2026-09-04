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
)
