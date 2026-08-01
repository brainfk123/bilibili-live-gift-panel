package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

//go:embed dist/index.html
var embeddedFS embed.FS

func writeJSON(w http.ResponseWriter, code int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func handleRoomInfo(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("roomId")
	if roomID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "缺少房间号"})
		return
	}
	info, err := getRoomInfo(roomID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": -1, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "roomId": info.RoomID, "buvid": info.Buvid, "token": info.Token, "hostList": info.HostList})
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func listenWithFallback(startPort, attempts int) (net.Listener, int, error) {
	if attempts < 1 {
		return nil, 0, fmt.Errorf("没有可用的端口尝试次数")
	}
	var lastErr error
	for offset := 0; offset < attempts; offset++ {
		port := startPort + offset
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("无法监听 %d-%d 端口：%w", startPort, startPort+attempts-1, lastErr)
}

func isExistingPanel(port int) bool {
	client := http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	return err == nil && resp.StatusCode == http.StatusOK && string(body) == "bilibili-live-gift-panel"
}

func waitForStartupError(message string) {
	fmt.Printf("\n启动失败：%s\n\n请按 Enter 键关闭窗口...\n", message)
	_, _ = fmt.Scanln()
}

func main() {
	indexHTML, err := embeddedFS.ReadFile("dist/index.html")
	if err != nil {
		waitForStartupError(fmt.Sprintf("内嵌页面读取失败：%v", err))
		return
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})

	http.HandleFunc("/api/room_info", handleRoomInfo)
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("bilibili-live-gift-panel"))
	})

	listener, port, err := listenWithFallback(12450, 10)
	if err != nil {
		waitForStartupError(err.Error())
		return
	}
	if port != 12450 && isExistingPanel(12450) {
		listener.Close()
		fmt.Println("检测到面板已经在运行，正在打开已有配置页面。")
		openBrowser("http://localhost:12450/?mode=config")
		return
	}
	if port != 12450 {
		fmt.Printf("默认端口 12450 已被占用，已改用端口 %d。\n", port)
	}

	go openBrowser(fmt.Sprintf("http://localhost:%d/?mode=config", port))

	fmt.Printf("Bilibili 直播礼物面板已启动\n")
	fmt.Printf("  配置面板（浏览器已自动打开）: http://localhost:%d/?mode=config\n", port)
	fmt.Printf("  OBS 浏览器源请加载: http://localhost:%d/?mode=display\n", port)
	fmt.Printf("  关闭本窗口即退出。\n")

	if err := http.Serve(listener, nil); err != nil {
		waitForStartupError(err.Error())
	}
}
