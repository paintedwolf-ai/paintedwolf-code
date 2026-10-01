package usernotice

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

var (
	stockFloorOnce sync.Once
	stockFloor     map[string]*Notification
	stockFloorErr  error
)

// notificationFloor loads embedded placement declarations once.
func notificationFloor() (map[string]*Notification, error) {
	stockFloorOnce.Do(func() {
		out := make(map[string]*Notification)
		ents, err := config.List(config.UserNoticesDir)
		if err != nil {
			stockFloorErr = fmt.Errorf("read embedded user notices: %w", err)
			return
		}
		for _, ent := range ents {
			if ent.IsDir() {
				continue
			}
			name := ent.Name()
			if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
				continue
			}
			stem := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
			if stem == defaultsStem {
				continue
			}
			data, err := config.Read(config.UserNoticesDir.Join(name))
			if err != nil {
				stockFloorErr = fmt.Errorf("read embedded %s: %w", name, err)
				return
			}
			entry, err := ParseEntry(data)
			if err != nil {
				stockFloorErr = fmt.Errorf("parse embedded %s: %w", name, err)
				return
			}
			out[stem] = entry.Notification
		}
		stockFloor = out
	})
	return stockFloor, stockFloorErr
}

// enforceNotificationFloor preserves embedded placement.
func enforceNotificationFloor(code string, entry Entry) error {
	floor, err := notificationFloor()
	if err != nil {
		return err
	}
	want, isStock := floor[code]
	if !isStock {
		// New codes own their placement.
		return nil
	}
	if !reflect.DeepEqual(want, entry.Notification) {
		return fmt.Errorf(
			"notification is host-managed for stock code %q and cannot be overridden (want %s, got %s)",
			code, describeNotification(want), describeNotification(entry.Notification),
		)
	}
	return nil
}

func describeNotification(n *Notification) string {
	if n == nil {
		return "none"
	}
	if !n.IsConditional() {
		return fmt.Sprintf("tier=%s scope=%s", n.Tier, n.Scope)
	}
	parts := make([]string, 0, len(n.Resolutions))
	for _, r := range n.Normalized() {
		parts = append(parts, fmt.Sprintf("%s:tier=%s,scope=%s", r.ID, r.Tier, r.Scope))
	}
	return fmt.Sprintf("discriminator=%s [%s]", n.Discriminator, strings.Join(parts, " "))
}
