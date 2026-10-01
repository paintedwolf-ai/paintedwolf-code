package designkit

import "sort"

// IconGroup is a named cluster of canonical icon ids for LLM discoverability.
type IconGroup struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Icons       []string `json:"icons"`
}

// IconGroups returns stable category buckets.
func IconGroups() []IconGroup {
	groups := []IconGroup{
		{ID: "actions", Label: "Actions", Description: "edit / save / share / clipboard", Icons: pickExisting(
			"plus", "minus", "x", "check", "copy", "clipboard", "clipboard-paste", "scissors", "save", "download", "upload",
			"trash-2", "square-pen", "pencil", "undo-2", "redo-2", "share-2", "external-link", "link", "paperclip", "printer", "send",
		)},
		{ID: "nav", Label: "Navigation", Description: "menus, chevrons, home, layout", Icons: pickExisting(
			"menu", "house", "layout-dashboard", "layout-grid", "layout-list",
			"arrow-left", "arrow-right", "arrow-up", "arrow-down", "chevron-left", "chevron-right", "chevron-up", "chevron-down",
			"chevrons-up", "chevrons-down", "ellipsis", "ellipsis-vertical",
		)},
		{ID: "status", Label: "Status", Description: "alerts, success, help", Icons: pickExisting(
			"triangle-alert", "circle-alert", "octagon-alert", "circle-check", "circle-x", "circle-help", "info", "ban",
			"badge-check", "shield-alert", "shield-check", "loader",
		)},
		{ID: "people", Label: "People & auth", Description: "users, login, locks", Icons: pickExisting(
			"user", "users", "user-plus", "user-minus", "user-check", "user-cog", "circle-user", "log-in", "log-out",
			"lock", "lock-open", "key", "shield", "fingerprint",
		)},
		{ID: "comms", Label: "Communication", Description: "mail, chat, phone, bell", Icons: pickExisting(
			"mail", "mail-open", "inbox", "message-square", "message-circle", "messages-square", "phone", "phone-call",
			"bell", "bell-off", "megaphone", "at-sign",
		)},
		{ID: "files", Label: "Files & folders", Description: "documents and directories", Icons: pickExisting(
			"file", "file-text", "file-code", "file-image", "file-plus", "file-search", "files",
			"folder", "folder-open", "folder-plus", "folder-search", "archive", "book", "book-open", "newspaper",
		)},
		{ID: "media", Label: "Media", Description: "image, video, audio controls", Icons: pickExisting(
			"image", "images", "camera", "video", "film", "play", "pause", "circle-play", "circle-pause", "circle-stop",
			"volume-2", "volume-x", "mic", "mic-off", "music", "headphones",
		)},
		{ID: "data", Label: "Data & charts", Description: "analytics and databases", Icons: pickExisting(
			"chart-bar", "chart-line", "chart-area", "chart-pie", "chart-column", "database", "server", "table", "binary",
		)},
		{ID: "commerce", Label: "Commerce", Description: "cart, payment, store", Icons: pickExisting(
			"shopping-cart", "shopping-bag", "shopping-basket", "store", "credit-card", "banknote", "wallet", "receipt",
			"tag", "badge-percent", "package", "truck",
		)},
		{ID: "time", Label: "Time & place", Description: "calendar, clock, map", Icons: pickExisting(
			"calendar", "calendar-days", "calendar-check", "calendar-clock", "clock", "timer", "hourglass",
			"map", "map-pin", "navigation", "compass", "globe",
		)},
		{ID: "devices", Label: "Devices & connectivity", Description: "hardware and network", Icons: pickExisting(
			"smartphone", "laptop", "monitor", "tablet", "watch", "cpu", "hard-drive", "wifi", "wifi-off", "bluetooth",
			"cloud", "cloud-upload", "cloud-download", "battery", "battery-charging", "power", "usb", "cast",
		)},
		{ID: "brand", Label: "Brand marks", Description: "optional social/platform marks", Icons: pickExisting(
			"github", "gitlab", "twitter", "youtube", "instagram", "facebook", "linkedin", "slack", "chrome",
		)},
	}
	return groups
}

func pickExisting(names ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = iconSearchTerm(n)
		if seen[n] {
			continue
		}
		if _, ok := iconByName[n]; !ok {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
