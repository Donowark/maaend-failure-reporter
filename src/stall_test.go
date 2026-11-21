package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecoveryAfterHeaderTimeout(t *testing.T) {
	for _, scenario := range []string{"recovers", "stays blocked", "404", "slow body", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			var reads atomic.Int32
			f, _, _ := fixture()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/maa/state":
					n := reads.Add(1)
					if scenario == "404" {
						http.NotFound(w, r)
						return
					}
					if scenario == "slow body" {
						w.Write([]byte("{"))
						w.(http.Flusher).Flush()
					}
					if scenario == "recovers" && n == 3 {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(100 * time.Millisecond):
						}
						json.NewEncoder(w).Encode(f.States)
						return
					}
					<-r.Context().Done()
				case "/api/heartbeat":
					w.Write([]byte(`{"ok":true}`))
				case "/api/config":
					json.NewEncoder(w).Encode(f.Config)
				case "/api/interface":
					json.NewEncoder(w).Encode(f.Interface)
				}
			}))
			defer srv.Close()
			c := testClient(t)
			c.recoveryTimeout = 300 * time.Millisecond
			budget := 2 * time.Second
			if scenario == "deadline" {
				budget = 25 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			s, err := fetchWithClient(ctx, serverPort(t, srv), c)
			expected := int32(3)
			if scenario == "404" || scenario == "deadline" {
				expected = 1
			}
			if scenario == "slow body" {
				expected = 2
			}
			if reads.Load() != expected {
				t.Fatalf("state reads %d want %d: %s", reads.Load(), expected, c.diagnosticReport())
			}
			if scenario == "recovers" {
				if err != nil || !reflect.DeepEqual(s.States, f.States) {
					t.Fatal(err, s.States)
				}
			} else if err == nil || s.States.Instances != nil {
				t.Fatal(err, s.States)
			}
			if scenario != "deadline" && !strings.Contains(c.diagnosticReport(), "心跳正常") {
				t.Fatal(c.diagnosticReport())
			}
		})
	}
}

func TestHeartbeatClassifications(t *testing.T) {
	for _, tc := range []struct {
		code           int
		body, expected string
	}{
		{200, `{"ok":true}`, "心跳正常"}, {200, `{}`, "格式不符"}, {404, "", "可能不支持"}, {503, "", "未取得有效心跳"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code); w.Write([]byte(tc.body)) }))
		c := testClient(t)
		c.probeHeartbeat(context.Background(), serverPort(t, srv))
		srv.Close()
		if !strings.Contains(c.diagnosticReport(), tc.expected) {
			t.Fatal(c.diagnosticReport())
		}
	}
}

func TestLoopbackListenerPIDs(t *testing.T) {
	table := make([]byte, 4)
	for i, addr := range [][]byte{{127, 0, 0, 1}, {0, 0, 0, 0}, {192, 168, 1, 1}, {127, 0, 0, 1}, {0, 0, 0, 0}} {
		row := make([]byte, 24)
		binary.LittleEndian.PutUint32(row, 2)
		copy(row[4:8], addr)
		binary.BigEndian.PutUint16(row[8:], 12701)
		binary.LittleEndian.PutUint32(row[20:], uint32(100+i))
		if i == 3 {
			binary.BigEndian.PutUint16(row[8:], 12702)
		}
		if i == 4 {
			binary.LittleEndian.PutUint32(row, 5)
		}
		table = append(table, row...)
	}
	binary.LittleEndian.PutUint32(table, 5)
	if got := loopbackListenerPIDs(table, 12701); !reflect.DeepEqual(got, []uint32{100, 101}) {
		t.Fatal(got)
	}
	for _, bad := range [][]byte{nil, table[:len(table)-1]} {
		if got := loopbackListenerPIDs(bad, 12701); len(got) != 0 {
			t.Fatal(got)
		}
	}
}
