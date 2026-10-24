package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture() (Snapshot, Instance, InstanceState) {
	i := Instance{ID: "tab1", Name: "日常", Tasks: []Task{{ID: "a", Name: "Transfer"}, {ID: "b", Name: "Farm"}, {ID: "c", Name: "Farm"}, {ID: "d", Name: "Later"}}}
	st := InstanceState{Running: true, Run: &RunState{Statuses: map[string]string{"a": "failed", "b": "succeeded", "c": "failed", "d": "pending"}}}
	var s Snapshot
	s.Config.Instances = []Instance{i}
	s.States.Instances = map[string]InstanceState{"tab1": st}
	s.Interface.Interface.Tasks = []Definition{{Name: "Transfer", Label: "$transfer"}, {Name: "Farm", Label: "基质刷取"}}
	s.Interface.Translations = map[string]map[string]string{"zh_cn": {"transfer": "🚚转交委托"}}
	return s, i, st
}
func TestReportPreservesRows(t *testing.T) {
	s, i, st := fixture()
	text, err := report(s, i, st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"失败：2", "成功：1", "🚚转交委托", "基质刷取", "任务 ID：c"} {
		if !strings.Contains(text, v) {
			t.Fatalf("missing %s", v)
		}
	}
	st.Run.Statuses["b"] = "failed"
	f, _, err := collect(i, st, s.Interface)
	if err != nil || len(f) != 3 {
		t.Fatal("duplicate names must not be deduplicated")
	}
}
func TestMissingStatusesIsError(t *testing.T) {
	s, i, st := fixture()
	st.Run = nil
	if _, err := report(s, i, st, time.Now()); err == nil {
		t.Fatal("missing status must not become no failures")
	}
	st.Run = &RunState{Statuses: map[string]string{}}
	text, err := report(s, i, st, time.Now())
	if err != nil || !strings.Contains(text, "不能据此认定全部任务成功") {
		t.Fatal(text, err)
	}
}
func TestUnknownTaskAndUnknownStatus(t *testing.T) {
	s, i, st := fixture()
	st.Run.Statuses["removed"] = "failed"
	f, _, err := collect(i, st, s.Interface)
	if err != nil || len(f) != 3 || f[2].ID != "removed" {
		t.Fatal(f, err)
	}
	st.Run.Statuses["a"] = "new-status"
	if _, _, err = collect(i, st, s.Interface); err == nil {
		t.Fatal("unknown status should fail")
	}
}
func TestInstanceSelection(t *testing.T) {
	s, _, _ := fixture()
	s.Config.Instances = append(s.Config.Instances, Instance{ID: "tab2", Name: "搬货"})
	if i, e := choose(s, ""); e != nil || i.ID != "tab1" {
		t.Fatal(i, e)
	}
	s.States.Instances["tab2"] = s.States.Instances["tab1"]
	if _, e := choose(s, ""); e == nil {
		t.Fatal("ambiguous running instances must fail")
	}
	if i, e := choose(s, "搬货"); e != nil || i.ID != "tab2" {
		t.Fatal(i, e)
	}
}
func TestReadOnlyHTTPAndWrite(t *testing.T) {
	fixtureS, _, _ := fixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("must be read-only")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/config":
			json.NewEncoder(w).Encode(fixtureS.Config)
		case "/api/maa/state":
			json.NewEncoder(w).Encode(fixtureS.States)
		case "/api/interface":
			json.NewEncoder(w).Encode(fixtureS.Interface)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(p)
	s, err := fetch(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	i, err := choose(s, "日常")
	if err != nil {
		t.Fatal(err)
	}
	text, err := report(s, i, s.States.Instances[i.ID], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p1, err := writeReport(dir, "失败任务", text)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := writeReport(dir, "失败任务", text)
	if err != nil || p1 == p2 {
		t.Fatal("reports must not overwrite", err)
	}
	b, _ := os.ReadFile(p1)
	if !strings.HasPrefix(string(b), "\ufeff") || !strings.Contains(string(b), "\r\n") {
		t.Fatal("UTF-8 BOM/CRLF expected")
	}
	if _, e := writeReport(dir+"/missing", "test", text); e == nil {
		t.Fatal("missing output directory must not report success")
	}
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(p)
	return port
}
func TestRetryTimeoutThenRecover(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		w.Write([]byte(`{"instances":[{"id":"ok"}]}`))
	}))
	defer srv.Close()
	c := &apiClient{timeout: 100 * time.Millisecond, attempts: 3, delay: time.Millisecond}
	var config Config
	if err := c.getJSON(context.Background(), serverPort(t, srv), "config", &config); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(config.Instances) != 1 || len(c.diagnostics) != 2 {
		t.Fatal(calls.Load(), config, c.diagnostics)
	}
}
func TestRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		calls   int
		success bool
	}{
		{"transient HTTP", 503, `busy`, 3, false},
		{"missing API", 404, `missing`, 1, false},
		{"bad JSON", 200, `not JSON`, 1, false},
		{"valid JSON", 200, `{"instances":[]}`, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &apiClient{timeout: time.Second, attempts: 3, delay: time.Millisecond}
			var config Config
			err := c.getJSON(context.Background(), serverPort(t, srv), "config", &config)
			if (err == nil) != tc.success || int(calls.Load()) != tc.calls || len(c.diagnostics) != tc.calls {
				t.Fatal(err, calls.Load(), c.diagnostics)
			}
		})
	}
}
func TestOverallDeadlineStopsRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer srv.Close()
	c := &apiClient{timeout: time.Second, attempts: 3, delay: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var config Config
	start := time.Now()
	err := c.getJSON(ctx, serverPort(t, srv), "config", &config)
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatal(err, calls.Load())
	}
}
func TestSlowResponseBodyRetriesWithoutPartialOutput(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Write([]byte(`{"instances":[`))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Write([]byte(`{"instances":[{"id":"final"}]}`))
	}))
	defer srv.Close()
	c := &apiClient{timeout: 100 * time.Millisecond, attempts: 3, delay: time.Millisecond}
	var config Config
	if err := c.getJSON(context.Background(), serverPort(t, srv), "config", &config); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(config.Instances) != 1 || config.Instances[0].ID != "final" {
		t.Fatal(config, calls.Load())
	}
}
