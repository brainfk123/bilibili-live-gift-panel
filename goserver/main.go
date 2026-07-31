package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"runtime"
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

func main() {
	indexHTML, err := embeddedFS.ReadFile("dist/index.html")
	if err != nil {
		log.Fatalf("failed to read embedded index.html: %v", err)
	}

	port := "12450"
	addr := fmt.Sprintf("127.0.0.1:%s", port)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})

	http.HandleFunc("/api/room_info", handleRoomInfo)

	go openBrowser(fmt.Sprintf("http://localhost:%s/?mode=config", port))

	fmt.Printf("Bilibili 直播礼物面板已启动\n")
	fmt.Printf("  配置面板（浏览器已自动打开）: http://localhost:%s/?mode=config\n", port)
	fmt.Printf("  OBS 浏览器源请加载: http://localhost:%s/?mode=display\n", port)
	fmt.Printf("  关闭本窗口即退出。\n")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
