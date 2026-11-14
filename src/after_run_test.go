package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAfterRunRecoversAndWaitsForTerminalState(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture, _, _ := fixture()
		switch r.URL.Path {
		case "/api/maa/state":
			attempt := attempts.Add(1)
			if attempt == 1 {
				<-r.Context().Done()
				return
			}
			state := fixture.States.Instances["tab1"]
			state.Run.QueuedTaskIDs = []int64{101, 102, 103, 104}
			state.Run.Mappings = map[int64]string{101: "a", 102: "b", 103: "c", 104: "d"}
			state.Running = attempt == 2
			if attempt >= 4 {
				state.Run.Statuses["d"] = "succeeded"
			}
			fixture.States.Instances["tab1"] = state
			json.NewEncoder(w).Encode(fixture.States)
		case "/api/config":
			json.NewEncoder(w).Encode(fixture.Config)
		case "/api/interface":
			json.NewEncoder(w).Encode(fixture.Interface)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := testClient(t).fetchAfterRun(ctx, serverPort(t, server), "日常")
	if err != nil {
		t.Fatal(err)
	}
	i, err := choose(s, "日常")
	if err != nil {
		t.Fatal(err)
	}
	output, err := report(s, i, s.States.Instances[i.ID], time.Now())
	if err != nil || !strings.Contains(output, "失败：2；成功：2") || !strings.Contains(output, "进入终态") {
		t.Fatal(err, output)
	}
	if attempts.Load() != 4 {
		t.Fatalf("expected one timeout, two incomplete states and one terminal state, got %d reads", attempts.Load())
	}
}

func TestAfterRunRejectsNewQueue(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture, _, _ := fixture()
		switch r.URL.Path {
		case "/api/maa/state":
			attempt := attempts.Add(1)
			state := fixture.States.Instances["tab1"]
			taskID := int64(100 + attempt)
			state.Run.QueuedTaskIDs = []int64{taskID}
			state.Run.Mappings = map[int64]string{taskID: "a"}
			state.Running = attempt == 1
			fixture.States.Instances["tab1"] = state
			json.NewEncoder(w).Encode(fixture.States)
		case "/api/config":
			json.NewEncoder(w).Encode(fixture.Config)
		case "/api/interface":
			json.NewEncoder(w).Encode(fixture.Interface)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := testClient(t).fetchAfterRun(ctx, serverPort(t, server), "tab1")
	if err == nil || !strings.Contains(err.Error(), "任务队列已变化") {
		t.Fatal(err)
	}
}

func TestAfterRunStillFailsWhenServerNeverResponds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Millisecond)
	defer cancel()
	s, err := testClient(t).fetchAfterRun(ctx, serverPort(t, server), "tab1")
	if err == nil || !strings.Contains(err.Error(), "等待本轮完成超时") || s.States.Instances != nil {
		t.Fatal(err, s.States)
	}
}
