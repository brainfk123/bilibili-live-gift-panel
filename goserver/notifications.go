package main

import (
	"fmt"
	"sync"
)

type notificationKind string

const (
	notificationServiceStarted     notificationKind = "service-started"
	notificationConfigPagesClosed  notificationKind = "config-pages-closed"
	notificationDisplayPagesClosed notificationKind = "display-pages-closed"
	notificationRoomConnected      notificationKind = "room-connected"
	notificationRoomDisconnected   notificationKind = "room-disconnected"
	notificationUpdateReady        notificationKind = "update-ready"
)

type desktopNotification struct {
	Title string
	Body  string
}

type notificationCenter struct {
	mu      sync.Mutex
	sink    func(desktopNotification)
	pending []desktopNotification
}

func newNotificationCenter() *notificationCenter {
	return &notificationCenter{pending: []desktopNotification{}}
}

func (center *notificationCenter) Publish(kind notificationKind, roomID string) {
	if center == nil {
		return
	}
	notification := makeDesktopNotification(kind, roomID)
	center.mu.Lock()
	sink := center.sink
	if sink == nil {
		center.pending = append(center.pending, notification)
		if len(center.pending) > 16 {
			center.pending = center.pending[len(center.pending)-16:]
		}
		center.mu.Unlock()
		return
	}
	center.mu.Unlock()
	sink(notification)
}

func (center *notificationCenter) AttachSink(sink func(desktopNotification)) {
	if center == nil {
		return
	}
	center.mu.Lock()
	center.sink = sink
	pending := append([]desktopNotification(nil), center.pending...)
	center.pending = center.pending[:0]
	center.mu.Unlock()
	if sink != nil {
		for _, notification := range pending {
			sink(notification)
		}
	}
}

func (center *notificationCenter) DetachSink() {
	if center == nil {
		return
	}
	center.mu.Lock()
	center.sink = nil
	center.mu.Unlock()
}

func makeDesktopNotification(kind notificationKind, roomID string) desktopNotification {
	switch kind {
	case notificationServiceStarted:
		return desktopNotification{
			Title: "直播礼物面板已启动",
			Body:  "后台服务正在系统托盘运行，关闭配置页面不会停止。",
		}
	case notificationConfigPagesClosed:
		return desktopNotification{
			Title: "配置页面已全部关闭",
			Body:  "后台服务仍在运行。单击托盘图标可重新打开配置页面。",
		}
	case notificationDisplayPagesClosed:
		return desktopNotification{
			Title: "OBS 属性面板已全部关闭",
			Body:  "当前没有打开的 OBS 属性面板页面。",
		}
	case notificationRoomConnected:
		return desktopNotification{
			Title: "直播间连接成功",
			Body:  fmt.Sprintf("已连接直播间 %s，后台正在接收礼物消息。", roomID),
		}
	case notificationRoomDisconnected:
		return desktopNotification{
			Title: "直播间连接已断开",
			Body:  fmt.Sprintf("直播间 %s 的连接已断开，后台将根据当前配置继续尝试连接。", roomID),
		}
	case notificationUpdateReady:
		return desktopNotification{
			Title: "新版本已下载",
			Body:  fmt.Sprintf("v%s 已准备好，退出后台程序后会自动安装。", roomID),
		}
	default:
		return desktopNotification{Title: "直播礼物面板", Body: "后台状态已更新。"}
	}
}
