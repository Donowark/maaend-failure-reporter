package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Trace callbacks can run on transport goroutines, so all observations are locked.
type requestTrace struct {
	mu                            sync.Mutex
	started                       time.Time
	connected, written, firstByte bool
	connectDuration               time.Duration
}

func (t *requestTrace) hooks() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		ConnectDone: func(_, _ string, err error) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if err == nil {
				t.connected = true
				t.connectDuration = time.Since(t.started)
			}
		},
		WroteRequest:         func(info httptrace.WroteRequestInfo) { t.mu.Lock(); defer t.mu.Unlock(); t.written = info.Err == nil },
		GotFirstResponseByte: func() { t.mu.Lock(); defer t.mu.Unlock(); t.firstByte = true },
	}
}
func (t *requestTrace) summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	phase := "TCP 连接未完成（检查端口、服务及本机拦截）"
	if t.connected {
		phase = "TCP 已连接，HTTP 请求尚未完整发出"
	}
	if t.written {
		phase = "HTTP 请求已发送，尚未收到响应首字节（可能为服务端阻塞或错误端口上的服务）"
	}
	if t.firstByte {
		phase = "已收到响应首字节"
	}
	return fmt.Sprintf("；连接阶段：%s；TCP 建连耗时：%s", phase, t.connectDuration.Round(time.Millisecond))
}

func (c *apiClient) quickJSON(ctx context.Context, port int, path string, out any) error {
	timeout := c.probeTimeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	quick := &apiClient{timeout: timeout, attempts: 1}
	err := quick.getJSON(ctx, port, path, out)
	c.diagnostics = append(c.diagnostics, quick.diagnostics...)
	return err
}

func (c *apiClient) probeService(ctx context.Context, port int) {
	// These probes classify a live-state failure; their data never replaces statuses.
	var config Config
	configErr := c.quickJSON(ctx, port, "config", &config)
	var pi InterfaceReply
	interfaceErr := c.quickJSON(ctx, port, "interface", &pi)
	if configErr == nil || interfaceErr == nil {
		c.diagnostics = append(c.diagnostics, "诊断：其他 HTTP 接口有响应，实时状态接口单独失败；优先检查状态锁或 MaaFramework 调用阻塞。")
	} else {
		c.diagnostics = append(c.diagnostics, "诊断：状态、配置和界面接口均失败；若 TCP 均已连接且请求已发送，更支持服务端整体无响应或连接到了错误实例。仅凭超时无法证明死锁。请提供同一时刻 mxu-tauri 日志及 MaaEnd/MXU 版本，核对 Web server listening 端口。")
	}
}

func validateConfig(c Config) error {
	if len(c.Instances) == 0 {
		return errors.New("配置缺少有效 instances")
	}
	seen := map[string]bool{}
	for _, i := range c.Instances {
		if i.ID == "" || seen[i.ID] {
			return errors.New("配置包含空或重复列表 ID")
		}
		seen[i.ID] = true
		tasks := map[string]bool{}
		for _, t := range i.Tasks {
			if t.ID == "" || tasks[t.ID] {
				return errors.New("配置包含空或重复任务 ID")
			}
			tasks[t.ID] = true
		}
	}
	return nil
}

type metadata struct {
	Schema  int       `json:"schema"`
	Port    int       `json:"port"`
	SavedAt time.Time `json:"saved_at"`
	Config  Config    `json:"config"`
}

func readMetadata(path string, port int) (metadata, error) {
	var m metadata
	if path == "" {
		return m, errors.New("没有可用的名称缓存路径")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if len(b) > 16*1024*1024 {
		return m, errors.New("名称缓存过大")
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Schema != 1 || m.Port != port || m.SavedAt.IsZero() {
		return m, errors.New("名称缓存版本或端口不匹配")
	}
	return m, validateConfig(m.Config)
}
func saveMetadata(path string, port int, s Snapshot) error {
	if path == "" {
		return nil
	}
	// Only IDs and display names are persisted. No runtime status, accounts or full config.
	config := Config{}
	for _, i := range s.Config.Instances {
		entry := Instance{ID: i.ID, Name: i.Name}
		for _, t := range i.Tasks {
			entry.Tasks = append(entry.Tasks, Task{ID: t.ID, Name: t.Name, Custom: taskLabel(t, s.Interface)})
		}
		config.Instances = append(config.Instances, entry)
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	b, err := json.MarshalIndent(metadata{1, port, time.Now(), config}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".metadata-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
