package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const panelHealthMarker = "bilibili-live-gift-panel"

type panelInstance struct {
	Port    int
	Version string
}

func inspectPanel(port int) (panelInstance, bool) {
	client := http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return panelInstance{}, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil || resp.StatusCode != http.StatusOK {
		return panelInstance{}, false
	}
	if strings.TrimSpace(string(body)) == panelHealthMarker {
		return panelInstance{Port: port}, true
	}
	var health struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &health) != nil || health.Name != panelHealthMarker {
		return panelInstance{}, false
	}
	return panelInstance{Port: port, Version: strings.TrimPrefix(strings.TrimSpace(health.Version), "v")}, true
}

func findPanelInstances() []panelInstance {
	instances := make([]panelInstance, 0, 2)
	for port := 12450; port < 12460; port++ {
		if instance, ok := inspectPanel(port); ok {
			instances = append(instances, instance)
		}
	}
	return instances
}

func panelConfigURL(instance panelInstance) string {
	return fmt.Sprintf("http://localhost:%d/?mode=config", instance.Port)
}

func compareRunningVersion(current, running string) int {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	running = strings.TrimPrefix(strings.TrimSpace(running), "v")
	if current == running && current != "" {
		return 0
	}
	comparison, err := compareStableVersions(current, running)
	if err == nil {
		return comparison
	}
	if running == "" && current != "" && current != "dev" {
		return 1
	}
	return -1
}

func panelToOpen(currentVersion string, instances []panelInstance) (panelInstance, bool) {
	var selected panelInstance
	found := false
	for _, instance := range instances {
		if compareRunningVersion(currentVersion, instance.Version) > 0 {
			continue
		}
		if !found || compareRunningVersion(instance.Version, selected.Version) > 0 {
			selected = instance
			found = true
		}
	}
	return selected, found
}

func requestPanelExit(instance panelInstance, currentVersion string) bool {
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/instance/exit", instance.Port), nil)
	if err != nil {
		return false
	}
	request.Header.Set("X-Bilibili-Panel-Takeover", strings.TrimPrefix(strings.TrimSpace(currentVersion), "v"))
	client := http.Client{Timeout: 500 * time.Millisecond}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusAccepted
}

func waitForPanelExit(port int) bool {
	for attempt := 0; attempt < 30; attempt++ {
		if _, running := inspectPanel(port); !running {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func newInstanceExitHandler(currentVersion string, exit chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			writeJSON(w, http.StatusForbidden, map[string]any{"code": -1, "message": "仅允许本机接管实例"})
			return
		}
		requestVersion := r.Header.Get("X-Bilibili-Panel-Takeover")
		comparison, versionErr := compareStableVersions(requestVersion, currentVersion)
		if versionErr != nil || comparison <= 0 {
			writeJSON(w, http.StatusConflict, map[string]any{"code": -1, "message": "仅允许更新版本接管当前实例"})
			return
		}
		select {
		case exit <- struct{}{}:
		default:
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"code": 0})
	}
}
