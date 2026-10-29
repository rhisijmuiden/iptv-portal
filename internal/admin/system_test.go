package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSystemPage(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)

	body := req("GET", "/system", nil).Body.String()
	for _, want := range []string{"<h1>System resources</h1>", `href="/system" class="active"`, `data-chart="cpu"`,
		`id="system-report"`, `"url": "/system/data?range=hour"`, `src="/static/system.js"`} {
		if !strings.Contains(body, want) {
			t.Errorf("system page is missing %q", want)
		}
	}
	if strings.Contains(req("GET", "/system?range=day", nil).Body.String(), `range=hour`) {
		t.Error("day range not kept")
	}

	w := req("GET", "/system/data?range=day", nil)
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var rep struct {
		Range   string           `json:"range"`
		History []map[string]any `json:"history"`
		Process struct {
			PID int `json:"pid"`
		} `json:"process"`
		Disk struct {
			Path string `json:"path"`
		} `json:"disk"`
	}
	if err := json.NewDecoder(w.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.Range != "day" || len(rep.History) != 1 || rep.Process.PID == 0 || rep.Disk.Path != st.Dir() {
		t.Errorf("report = %+v", rep)
	}
}
