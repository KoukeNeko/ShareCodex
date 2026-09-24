package admin

import (
	"net/http"
	"strings"
	"time"
)

// Console languages. English is the default; the choice is kept in a cookie.
const (
	langCookie  = "sharecodex_lang"
	defaultLang = "en"
)

var langNames = []struct{ ID, Label string }{
	{"en", "English"},
	{"zh-TW", "繁體中文"},
}

// Values containing %s or %d are printf formats.
var dictionaries = map[string]map[string]string{
	"en": {
		"appTitle":        "ShareCodex Admin",
		"navOverview":     "Overview",
		"navPeople":       "Members",
		"navAccounts":     "Accounts",
		"navDevices":      "Devices",
		"logout":          "Sign out",
		"login":           "Sign in",
		"loginTitle":      "Admin sign-in",
		"password":        "Admin password",
		"wrongPassword":   "Wrong password",
		"noAccounts":      "No accounts yet",
		"noQuota":         "No quota data yet",
		"reset":           "Reset",
		"resetsAt":        "Resets %s",
		"member":          "Member",
		"estimatedUsage":  "Estimated usage",
		"allotted":        "Allotted",
		"overAllotment":   "Over allotment",
		"unattributed":    "Unattributed",
		"model":           "Model",
		"requests":        "Requests",
		"tokens":          "Tokens",
		"inviteTitle":     "Join link for %s",
		"inviteNote":      "Single use, valid until %s. It can't be shown again after you leave this page.",
		"name":            "Name",
		"addPerson":       "Add member",
		"devices":         "Devices",
		"createInvite":    "Create join link",
		"noPeople":        "No members",
		"nameRequired":    "Enter a name",
		"nameTooLong":     "Names can be at most 50 characters",
		"nameTaken":       "A member with this name already exists",
		"accountName":     "Account name",
		"rename":          "Rename",
		"weight":          "Weight",
		"weightFor":       "Weight for %s",
		"notAllotted":     "Not allotted",
		"addMember":       "Add member…",
		"newMemberWeight": "Weight for the new member",
		"saveShares":      "Save shares",
		"labelLength":     "Names must be 1 to 50 characters",
		"weightRange":     "Weights must be numbers from 0 to 100",
		"personNotFound":  "That member no longer exists",
		"device":          "Device",
		"platform":        "System",
		"lastSeen":        "Last seen",
		"revoked":         "Revoked",
		"revoke":          "Revoke",
		"revokeConfirm":   "Revoke this device? It can no longer sync; usage it already uploaded is kept.",
		"noDevices":       "No devices",
		"fiveHour":        "5 hours",
		"weekly":          "Weekly",
		"days":            "%d days",
		"hours":           "%d hours",
		"minutes":         "%d min",
		"language":        "Language",
	},
	"zh-TW": {
		"appTitle":        "ShareCodex 管理",
		"navOverview":     "總覽",
		"navPeople":       "成員",
		"navAccounts":     "帳號",
		"navDevices":      "裝置",
		"logout":          "登出",
		"login":           "登入",
		"loginTitle":      "管理登入",
		"password":        "管理密碼",
		"wrongPassword":   "密碼錯誤",
		"noAccounts":      "尚無帳號",
		"noQuota":         "尚無額度資料",
		"reset":           "已重置",
		"resetsAt":        "%s 重置",
		"member":          "成員",
		"estimatedUsage":  "估計用量",
		"allotted":        "分配",
		"overAllotment":   "超出分配",
		"unattributed":    "未歸屬",
		"model":           "模型",
		"requests":        "請求",
		"tokens":          "Tokens",
		"inviteTitle":     "%s 的加入連結",
		"inviteNote":      "只能使用一次，%s 前有效。關閉此頁後無法再次顯示。",
		"name":            "名稱",
		"addPerson":       "新增成員",
		"devices":         "裝置",
		"createInvite":    "產生加入連結",
		"noPeople":        "沒有成員",
		"nameRequired":    "請輸入名稱",
		"nameTooLong":     "名稱不可超過 50 字",
		"nameTaken":       "已有同名成員",
		"accountName":     "帳號名稱",
		"rename":          "重新命名",
		"weight":          "權重",
		"weightFor":       "%s 的權重",
		"notAllotted":     "不分配",
		"addMember":       "加入成員…",
		"newMemberWeight": "新成員的權重",
		"saveShares":      "儲存分配",
		"labelLength":     "名稱需為 1 到 50 字",
		"weightRange":     "權重需為 0 到 100 的數字",
		"personNotFound":  "找不到該成員",
		"device":          "裝置",
		"platform":        "系統",
		"lastSeen":        "最後連線",
		"revoked":         "已撤銷",
		"revoke":          "撤銷",
		"revokeConfirm":   "撤銷這台裝置？該裝置將無法再同步，已上傳的紀錄會保留。",
		"noDevices":       "沒有裝置",
		"fiveHour":        "5 小時",
		"weekly":          "每週",
		"days":            "%d 天",
		"hours":           "%d 小時",
		"minutes":         "%d 分鐘",
		"language":        "語言",
	},
}

func requestLang(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil {
		if _, ok := dictionaries[c.Value]; ok {
			return c.Value
		}
	}
	return defaultLang
}

// setLang stores the chosen language and returns to the page the switch was
// on. Only console paths are accepted as the destination.
func (c *Console) setLang(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	if _, ok := dictionaries[lang]; !ok {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     langCookie,
		Value:    lang,
		Path:     "/admin",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   strings.HasPrefix(c.publicURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "/admin/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		next = "/admin/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
