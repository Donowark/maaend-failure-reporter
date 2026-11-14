package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// The caller must launch this as the last task with wait_for_exit disabled.
// The queue identifies the first run we can observe, including across retries.
func (c *apiClient) fetchAfterRun(ctx context.Context, port int, selector string) (Snapshot, error) {
	s := Snapshot{AfterRun: true}
	var instanceID string
	var queue []int64
	var lastReadError error
	for ctx.Err() == nil {
		var live States
		lastReadError = c.quickJSON(ctx, port, "maa/state", &live)
		if lastReadError == nil {
			if live.Instances == nil {
				return s, errors.New("实时状态缺少 instances 字段")
			}
			s.States = live
			s.ObservedAt = time.Now()
			if instanceID == "" {
				c.loadMetadata(ctx, port, &s)
				instance, err := choose(s, selector)
				if err != nil {
					return s, err
				}
				instanceID = instance.ID
			}
			state, ok := live.Instances[instanceID]
			if !ok || state.Run == nil || len(state.Run.QueuedTaskIDs) == 0 || state.Run.Statuses == nil {
				return s, errors.New("指定列表缺少可识别的任务队列或实时状态，无法确认本轮运行")
			}
			if queue == nil {
				queue = slices.Clone(state.Run.QueuedTaskIDs)
			} else if !slices.Equal(queue, state.Run.QueuedTaskIDs) {
				return s, errors.New("等待期间任务队列已变化，无法确认仍是同一轮运行")
			}
			finished := !state.Running
			for _, taskID := range queue {
				selectedID, ok := state.Run.Mappings[taskID]
				if !ok || selectedID == "" {
					return s, fmt.Errorf("队列任务 %d 缺少列表任务 ID，无法确认本轮结果完整", taskID)
				}
				status := state.Run.Statuses[selectedID]
				finished = finished && (status == "succeeded" || status == "failed")
			}
			if finished {
				return s, nil
			}
		} else if ctx.Err() == nil && !transientError(lastReadError) {
			return s, lastReadError
		}

		timer := time.NewTimer(c.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if lastReadError != nil {
		return s, fmt.Errorf("等待本轮完成超时，最近一次实时状态读取失败：%w", lastReadError)
	}
	return s, fmt.Errorf("等待本轮完成超时：%w", ctx.Err())
}
