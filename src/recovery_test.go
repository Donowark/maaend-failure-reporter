package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T) *apiClient {
	t.Helper()
	return &apiClient{timeout: 50 * time.Millisecond, attempts: 2, delay: time.Millisecond, probeTimeout: 50 * time.Millisecond, metadataPath: filepath.Join(t.TempDir(), "metadata.json")}
}
func TestBlockedConfigUsesLiveFailuresAndCachedNames(t *testing.T) {
	fixtureS, _, _ := fixture()
	var stateRead atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/maa/state":
			stateRead.Store(true)
			// This run has a different failure from the cached snapshot.
			fresh := InstanceState{Running: true, Run: &RunState{Statuses: map[string]string{"b": "failed", "c": "succeeded"}}}
			json.NewEncoder(w).Encode(States{map[string]InstanceState{"tab1": fresh}})
		case "/api/config":
			if !stateRead.Load() {
				t.Error("configuration must not block acquiring live statuses")
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t)
	port := serverPort(t, srv)
	if err := saveMetadata(c.metadataPath, port, fixtureS); err != nil {
		t.Fatal(err)
	}
	s, err := fetchWithClient(context.Background(), port, c)
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
	for _, needle := range []string{"失败：1", "成功：1", "基质刷取", "任务 ID：b", "元数据缓存", "实时状态读取时间"} {
		if !strings.Contains(text, needle) {
			t.Fatal(needle, text)
		}
	}
	if strings.Contains(text, "任务 ID：a") {
		t.Fatal("historical failed status leaked", text)
	}
	if !strings.Contains(c.diagnosticReport(), "HTTP 请求已发送，尚未收到响应首字节") {
		t.Fatal(c.diagnosticReport())
	}
}

func TestConfigSnapshotFallback(t *testing.T) {
	fixtureS, _, _ := fixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/maa/state":
			json.NewEncoder(w).Encode(fixtureS.States)
		case "/api/config":
			<-r.Context().Done()
		case "/api/config/snapshot":
			json.NewEncoder(w).Encode(map[string]any{"config": fixtureS.Config, "revision": "current"})
		case "/api/interface":
			json.NewEncoder(w).Encode(fixtureS.Interface)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t)
	port := serverPort(t, srv)
	s, err := fetchWithClient(context.Background(), port, c)
	if err != nil || !strings.Contains(s.MetadataWarning, "config/snapshot") {
		t.Fatal(err, s.MetadataWarning)
	}
	if _, err = readMetadata(c.metadataPath, port); err != nil {
		t.Fatal("successful fallback must populate metadata", err)
	}
}

func TestNoConfigOrCachePreservesFailureIDs(t *testing.T) {
	fixtureS, _, _ := fixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/maa/state" {
			json.NewEncoder(w).Encode(fixtureS.States)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	s, err := fetchWithClient(context.Background(), serverPort(t, srv), testClient(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = choose(s, "日常"); err == nil {
		t.Fatal("must not guess a missing instance name")
	}
	i, err := choose(s, "tab1")
	if err != nil {
		t.Fatal(err)
	}
	text, err := report(s, i, s.States.Instances[i.ID], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "失败：2") || !strings.Contains(text, "任务 ID：a") || !strings.Contains(text, "任务 ID：c") {
		t.Fatal(text)
	}
}

func TestLiveStateFailureNeverUsesCachedStatuses(t *testing.T) {
	fixtureS, _, _ := fixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/config" {
			json.NewEncoder(w).Encode(fixtureS.Config)
			return
		}
		if r.URL.Path == "/api/interface" {
			json.NewEncoder(w).Encode(fixtureS.Interface)
			return
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := testClient(t)
	port := serverPort(t, srv)
	if err := saveMetadata(c.metadataPath, port, fixtureS); err != nil {
		t.Fatal(err)
	}
	_, err := fetchWithClient(context.Background(), port, c)
	if err == nil || !strings.Contains(err.Error(), "不能使用历史状态") {
		t.Fatal(err)
	}
	if !strings.Contains(c.diagnosticReport(), "实时状态接口单独失败") {
		t.Fatal(c.diagnosticReport())
	}
}

func TestAllEndpointsBlockedClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := testClient(t)
	_, err := fetchWithClient(context.Background(), serverPort(t, srv), c)
	if err == nil || !strings.Contains(c.diagnosticReport(), "状态、配置和界面接口均失败") {
		t.Fatal(err, c.diagnosticReport())
	}
}

func TestMetadataIsolationAndValidation(t *testing.T) {
	s, _, _ := fixture()
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := saveMetadata(path, 12701, s); err != nil {
		t.Fatal(err)
	}
	if err := saveMetadata(path, 12701, s); err != nil {
		t.Fatal("atomic replacement failed", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"failed", "succeeded", "statuses", "translations", "password"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatal("cache contains more than names and IDs", string(b))
		}
	}
	if _, err = readMetadata(path, 12702); err == nil {
		t.Fatal("must reject another port's cache")
	}
	if err = os.WriteFile(path, []byte(`{"schema":1,"port":12701,"saved_at":"2026-11-07T01:00:00Z","config":{"instances":[{"id":"x"},{"id":"x"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readMetadata(path, 12701); err == nil {
		t.Fatal("must reject duplicate IDs")
	}
}
