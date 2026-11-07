package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Task struct {
	ID     string `json:"id"`
	Name   string `json:"taskName"`
	Custom string `json:"customName"`
}
type Instance struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Tasks []Task `json:"tasks"`
}
type Config struct {
	Instances []Instance `json:"instances"`
}
type RunState struct {
	Statuses map[string]string `json:"statuses"`
}
type InstanceState struct {
	Running bool      `json:"is_running"`
	Run     *RunState `json:"task_run_state"`
}
type States struct {
	Instances map[string]InstanceState `json:"instances"`
}
type Definition struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}
type InterfaceReply struct {
	Interface struct {
		Tasks []Definition `json:"task"`
	} `json:"interface"`
	Translations map[string]map[string]string `json:"translations"`
}
type Snapshot struct {
	Config          Config
	States          States
	Interface       InterfaceReply
	LabelWarning    string
	MetadataWarning string
	ObservedAt      time.Time
}
type Failure struct {
	Position int
	ID, Name string
}

const version = "0.3.0"
const overallTimeout = 100 * time.Second

type apiClient struct {
	timeout      time.Duration
	attempts     int
	delay        time.Duration
	diagnostics  []string
	metadataPath string
	probeTimeout time.Duration
}

func newAPIClient() *apiClient {
	return &apiClient{timeout: 10 * time.Second, attempts: 3, delay: time.Second, probeTimeout: 3 * time.Second}
}
func transientError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}
func (c *apiClient) getJSON(ctx context.Context, port int, path string, out any) error {
	// Only loopback GETs. Do not inherit an HTTP proxy or follow redirects.
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: c.timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	started := time.Now()
	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("本机接口 /api/%s 整体等待期限已到：%w", path, err)
		}
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/%s", port, path), nil)
		if err != nil {
			return err
		}
		trace := &requestTrace{started: start}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace.hooks()))
		retry := false
		resp, err := client.Do(req)
		if err != nil {
			retry = transientError(err)
		} else {
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
				switch resp.StatusCode {
				case 408, 429, 500, 502, 503, 504:
					retry = true
				}
			} else {
				// Decode into temporary bytes so failed reads never contaminate the output.
				var raw json.RawMessage
				err = json.NewDecoder(io.LimitReader(resp.Body, 16*1024*1024)).Decode(&raw)
				if err == nil {
					err = json.Unmarshal(raw, out)
				}
				retry = transientError(err)
			}
			resp.Body.Close()
		}
		result := "成功"
		if err != nil {
			result = err.Error()
		}
		c.diagnostics = append(c.diagnostics, fmt.Sprintf("/api/%s 第 %d/%d 次；耗时 %s；结果：%s", path, attempt, c.attempts, time.Since(start).Round(time.Millisecond), result)+trace.summary())
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || attempt == c.attempts || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(c.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("本机接口 /api/%s 重试等待中止：%w", path, ctx.Err())
		case <-timer.C:
		}
	}
	return fmt.Errorf("本机接口 /api/%s 请求失败（累计耗时 %s）：%w", path, time.Since(started).Round(time.Millisecond), lastErr)
}
func (c *apiClient) diagnosticReport() string {
	return fmt.Sprintf("\n请求诊断（程序 v%s）\n单次超时：%s；每个接口最多尝试：%d 次；重试间隔：%s；整体期限：%s\n%s\n", version, c.timeout, c.attempts, c.delay, overallTimeout, strings.Join(c.diagnostics, "\n"))
}
func fetch(ctx context.Context, port int) (Snapshot, error) {
	return fetchWithClient(ctx, port, newAPIClient())
}
func fetchWithClient(ctx context.Context, port int, client *apiClient) (Snapshot, error) {
	var s Snapshot
	// Acquire fresh statuses before asking for optional task names/configuration.
	if err := client.getJSON(ctx, port, "maa/state", &s.States); err != nil {
		client.probeService(ctx, port)
		return s, fmt.Errorf("实时任务状态读取失败，不能使用历史状态替代：%w", err)
	}
	s.ObservedAt = time.Now()
	if s.States.Instances == nil {
		return s, errors.New("当前 MXU 的状态接口不兼容：缺少 instances 字段")
	}
	configErr := client.getJSON(ctx, port, "config", &s.Config)
	if configErr == nil {
		configErr = validateConfig(s.Config)
	}
	if configErr != nil {
		// Newer MXU provides a revisioned snapshot; older versions return 404.
		var snapshot struct {
			Config Config `json:"config"`
		}
		alternateErr := client.quickJSON(ctx, port, "config/snapshot", &snapshot)
		if alternateErr == nil {
			alternateErr = validateConfig(snapshot.Config)
		}
		if alternateErr == nil {
			s.Config = snapshot.Config
			s.MetadataWarning = "主配置接口不可用，已使用本次 /api/config/snapshot 的配置。"
		} else {
			cached, cacheErr := readMetadata(client.metadataPath, port)
			if cacheErr == nil {
				s.Config = cached.Config
				// Cached labels only; never deserialize or use cached run states.
				s.MetadataWarning = fmt.Sprintf("配置接口不可用，名称和顺序来自 %s 的元数据缓存，可能已过时；成功/失败状态来自本次实时读取。", cached.SavedAt.Format(time.RFC3339))
			} else {
				// A live instance ID still permits complete failure-ID reporting on first use.
				ids := make([]string, 0, len(s.States.Instances))
				for id := range s.States.Instances {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				s.Config = Config{}
				for _, id := range ids {
					s.Config.Instances = append(s.Config.Instances, Instance{ID: id, Name: "名称不可用（ID：" + id + "）"})
				}
				s.MetadataWarning = "配置和名称缓存均不可用：只能按列表 ID 选择，失败项以任务 ID 保留；不根据列表名猜测。请在空闲时运行 Check.cmd 建立名称缓存。"
			}
			client.diagnostics = append(client.diagnostics, fmt.Sprintf("配置降级：主接口：%v；备用接口：%v；名称缓存：%v", configErr, alternateErr, cacheErr))
		}
	}
	// Translations are optional and must not consume the full retry budget.
	if err := client.quickJSON(ctx, port, "interface", &s.Interface); err != nil {
		s.LabelWarning = "界面任务名称翻译不可用；使用配置中的任务名。"
	}
	// Never replace a good cache with fallback/cached metadata.
	if configErr == nil || strings.Contains(s.MetadataWarning, "本次 /api/config/snapshot") {
		if err := saveMetadata(client.metadataPath, port, s); err != nil {
			client.diagnostics = append(client.diagnostics, "名称缓存保存失败（不影响本次实时清单）："+err.Error())
		}
	}
	return s, nil
}
func choose(s Snapshot, selector string) (Instance, error) {
	if selector != "" {
		var hits []Instance
		for _, i := range s.Config.Instances {
			if i.ID == selector || i.Name == selector {
				hits = append(hits, i)
			}
		}
		if len(hits) != 1 {
			return Instance{}, fmt.Errorf("--instance %q 匹配到 %d 个列表，请使用准确的列表名或 ID", selector, len(hits))
		}
		return hits[0], nil
	}
	var running []Instance
	for _, i := range s.Config.Instances {
		if s.States.Instances[i.ID].Running {
			running = append(running, i)
		}
	}
	if len(running) == 1 {
		return running[0], nil
	}
	if len(running) > 1 {
		return Instance{}, errors.New("多个任务列表正在运行，请用 --instance 指定列表名或 ID")
	}
	if len(s.Config.Instances) == 1 {
		return s.Config.Instances[0], nil
	}
	return Instance{}, errors.New("当前没有唯一的运行中列表，请用 --instance 指定列表名或 ID；可以先运行 --check 查看列表")
}
func taskLabel(t Task, pi InterfaceReply) string {
	if t.Custom != "" {
		return t.Custom
	}
	for _, d := range pi.Interface.Tasks {
		if d.Name != t.Name {
			continue
		}
		label := d.Label
		if strings.HasPrefix(label, "$") {
			key := strings.TrimPrefix(label, "$")
			for _, lang := range []string{"zh_cn", "zh-CN", "zh_cn.json", "en_us", "en-US"} {
				if v := pi.Translations[lang][key]; v != "" {
					return v
				}
			}
			// Language files can use different keys; keep the stable task name if no Chinese label.
			return t.Name
		}
		if label != "" {
			return label
		}
	}
	return t.Name
}
func collect(i Instance, st InstanceState, pi InterfaceReply) ([]Failure, map[string]int, error) {
	if st.Run == nil || st.Run.Statuses == nil {
		return nil, nil, errors.New("状态接口缺少 task_run_state.statuses，不能可靠识别失败任务；请提供版本信息和诊断文件")
	}
	counts := map[string]int{}
	var failed []Failure
	known := map[string]bool{}
	for n, t := range i.Tasks {
		known[t.ID] = true
		status, ok := st.Run.Statuses[t.ID]
		if !ok {
			continue
		}
		counts[status]++
		if status == "failed" {
			failed = append(failed, Failure{n + 1, t.ID, taskLabel(t, pi)})
		}
	}
	// Do not silently drop failed records if configuration changed during execution.
	var unmatched []string
	for id, status := range st.Run.Statuses {
		if !known[id] {
			counts[status]++
			if status == "failed" {
				unmatched = append(unmatched, id)
			}
		}
	}
	sort.Strings(unmatched)
	for _, id := range unmatched {
		failed = append(failed, Failure{0, id, "任务名不可用（任务配置可能已改变）"})
	}
	for status := range counts {
		switch status {
		case "idle", "pending", "running", "succeeded", "failed":
		default:
			return nil, nil, fmt.Errorf("遇到未知任务状态 %q，不能可靠生成清单", status)
		}
	}
	return failed, counts, nil
}
func report(s Snapshot, i Instance, st InstanceState, when time.Time) (string, error) {
	failed, counts, err := collect(i, st, s.Interface)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MaaEnd 失败任务清单\n生成时间：%s\n任务列表：%s\n列表 ID：%s\n来源：MXU 本机任务状态接口\n\n", when.Format("2006-01-02 15:04:05 -0700"), i.Name, i.ID)
	if !s.ObservedAt.IsZero() {
		fmt.Fprintf(&b, "实时状态读取时间：%s\n", s.ObservedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "失败：%d；成功：%d；运行中：%d；等待中：%d\n", len(failed), counts["succeeded"], counts["running"], counts["pending"])
	b.WriteString("统计范围：调用时这个列表已登记的任务状态；尚未执行的后续任务不计入失败。\n\n")
	if len(st.Run.Statuses) == 0 {
		b.WriteString("当前列表没有已登记的运行状态，不能据此认定全部任务成功。\n")
	} else if len(failed) == 0 {
		b.WriteString("当前状态中未发现失败任务。\n")
	} else {
		for n, f := range failed {
			fmt.Fprintf(&b, "%d. %s\n   列表位置：%d；任务 ID：%s\n", n+1, f.Name, f.Position, f.ID)
		}
	}
	if s.MetadataWarning != "" {
		fmt.Fprintf(&b, "\n说明：%s\n", s.MetadataWarning)
	}
	if s.LabelWarning != "" {
		fmt.Fprintf(&b, "\n说明：%s\n", s.LabelWarning)
	}
	return b.String(), nil
}
func checkReport(s Snapshot, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MaaEnd 兼容性与桌面写入检查\n时间：%s\n本机端口：%d\n接口连接成功。此文件写入成功表示本次运行具备目标目录写入权限。\n\n", time.Now().Format(time.RFC3339), port)
	for _, i := range s.Config.Instances {
		st, ok := s.States.Instances[i.ID]
		fmt.Fprintf(&b, "列表：%s\nID：%s\n正在运行：%t\n", i.Name, i.ID, st.Running)
		if !ok {
			b.WriteString("当前没有后端运行实例；请先执行一次此列表，再检查。\n\n")
			continue
		}
		f, c, e := collect(i, st, s.Interface)
		if e != nil {
			fmt.Fprintf(&b, "状态格式：不兼容，%s\n\n", e)
		} else {
			fmt.Fprintf(&b, "状态格式：兼容；登记任务 %d；失败 %d；成功 %d\n\n", len(st.Run.Statuses), len(f), c["succeeded"])
		}
	}
	if s.MetadataWarning != "" {
		fmt.Fprintf(&b, "\n说明：%s\n", s.MetadataWarning)
	}
	b.WriteString("自动运行时建议明确指定 --instance 列表名；多列表并行时必须指定。\n")
	return b.String()
}
func writeReport(dir, kind, content string) (string, error) {
	if dir == "" {
		return "", errors.New("目标目录为空")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("目标路径不是目录")
	}
	// Exclusive temporary file + unique final name. Existing reports are never overwritten.
	f, err := os.CreateTemp(dir, ".maaend-report-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.WriteString("\ufeff" + strings.ReplaceAll(content, "\n", "\r\n"))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("MaaEnd_%s_%s_%s.txt", kind, time.Now().Format("20060102_150405"), strings.TrimSuffix(strings.TrimPrefix(filepath.Base(tmp), ".maaend-report-"), ".tmp")))
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}
func execute() int {
	port := flag.Int("port", 12701, "MXU 本机 Web 服务端口")
	instance := flag.String("instance", "", "任务列表名或 ID；默认选择唯一运行中的列表")
	out := flag.String("output-dir", "", "覆盖报告目录；默认使用 Windows 实际桌面路径")
	check := flag.Bool("check", false, "检查接口并输出列表 ID 与权限诊断")
	strict := flag.Bool("strict", false, "检测或写入失败时返回非零退出码；默认记录异常后退出 0，让 MaaEnd 继续")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "端口必须在 1..65535 范围内")
		return 2
	}
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "存在未识别的参数；含空格的列表名须加引号")
		return 2
	}
	dir := *out
	var err error
	if dir == "" {
		dir, err = desktopPath()
	}
	ctx, cancel := context.WithTimeout(context.Background(), overallTimeout)
	defer cancel()
	client := newAPIClient()
	if base := fallbackPath(); base != "" {
		client.metadataPath = filepath.Join(base, fmt.Sprintf("metadata-%d.json", *port))
	}
	var content string
	kind := "失败任务"
	if err == nil {
		var s Snapshot
		s, err = fetchWithClient(ctx, *port, client)
		if err == nil {
			if *check {
				content = checkReport(s, *port)
				kind = "检查"
			} else {
				var i Instance
				i, err = choose(s, *instance)
				if err == nil {
					st, ok := s.States.Instances[i.ID]
					if !ok {
						err = errors.New("选定列表尚无后端运行状态，请在 MaaEnd 执行该列表后再导出")
					} else {
						content, err = report(s, i, st, time.Now())
					}
				}
			}
		}
	}
	if err != nil {
		kind = "导出异常"
		content = fmt.Sprintf("MaaEnd 失败任务导出未完成\n时间：%s\n原因：%s\n\n未将识别失败当成零个失败任务。\n请确认 MaaEnd 已启动，设置中的 Web 服务已启用、端口与 --port 相符，以及当前 MXU 支持 /api/maa/state 中的逐任务状态。\n执行 --check 可查看列表 ID；使用 --instance 指定目标列表。\n默认不会阻止后续 MaaEnd 任务；修复后重新导出。\n", time.Now().Format(time.RFC3339), err)
		fmt.Fprintln(os.Stderr, err)
	}
	content += client.diagnosticReport()
	originalErr := err
	path, writeErr := writeReport(dir, kind, content)
	if writeErr != nil {
		fmt.Fprintln(os.Stderr, "桌面/目标目录写入失败：", writeErr)
		fallback := fallbackPath()
		if fallback != "" {
			if e := os.MkdirAll(fallback, 0700); e == nil {
				content = "桌面/指定目录保存失败，以下是备用位置的报告。\n原因：" + writeErr.Error() + "\n\n" + content
				path, err = writeReport(fallback, kind, content)
				if err != nil {
					fmt.Fprintln(os.Stderr, "备用位置保存失败：", err)
				}
			}
		}
	}
	if path != "" {
		fmt.Println("报告已保存：", path)
	}
	if *strict && (originalErr != nil || writeErr != nil) {
		return 1
	}
	return 0
}
func main() { os.Exit(execute()) }
