package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chat_image/app/service"
)

// ── V1Seconds:Sora 发字符串,老客户端发数字,两种都要收 ────────────────────

func TestV1SecondsAcceptsBothShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
		ok   bool
	}{
		{"数字(老客户端)", `{"seconds":4}`, 4, true},
		{"字符串(Sora/new-api 转发)", `{"seconds":"4"}`, 4, true},
		{"缺省", `{}`, 0, true},
		{"null", `{"seconds":null}`, 0, true},
		{"非整数拒绝", `{"seconds":"four"}`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req V1VideoRequest
			err := json.Unmarshal([]byte(tc.body), &req)
			if tc.ok != (err == nil) {
				t.Fatalf("err=%v, want ok=%v", err, tc.ok)
			}
			if tc.ok && int(req.Seconds) != tc.want {
				t.Errorf("seconds=%d, want %d", req.Seconds, tc.want)
			}
		})
	}
}

// TestVideoRequestToleratesUnknownFields 网关转发会附加自有字段;严格拒收
// 意味着这个服务没法待在任何网关后面 —— 这正是之前 new-api 转发被
// "invalid JSON body" 挡掉的根因。
func TestVideoRequestToleratesUnknownFields(t *testing.T) {
	body := `{"model":"local-video","prompt":"pan","seconds":"4","metadata":{"gw":"new-api"},"user":"u1"}`
	var req V1VideoRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("未知字段不该拒: %v", err)
	}
	if req.Prompt != "pan" || int(req.Seconds) != 4 {
		t.Errorf("标准字段解析错: %+v", req)
	}
}

// ── 图片同步分流:bearer 拿 OpenAI 同步形状,HMAC 维持异步任务体 ────────────

// syncTestServer 造一个带任务的最小 Server(无 Comfy、worker=0 不起工人)。
func syncTestServer(t *testing.T) (*service.Server, string) {
	t.Helper()
	workflows := map[string]service.WorkflowConfig{
		"t2i": {ID: "t2i"},
	}
	s := service.NewServer(workflows, nil, service.ServiceConfig{StorageMode: service.StorageModeLocal}, 0, t.TempDir(), nil)
	task, err := s.CreateTask(service.CreateTaskRequest{WorkflowID: "t2i", Prompt: "x"})
	if err != nil {
		t.Fatalf("造任务失败: %v", err)
	}
	return s, task.ID
}

// TestRespondImageSyncCompleted 任务完成 → OpenAI 标准 {created, data:[{url}]}。
func TestRespondImageSyncCompleted(t *testing.T) {
	s, taskID := syncTestServer(t)
	s.UpdateTask(taskID, func(task *service.Task) {
		task.Status = "completed"
		task.Outputs = []service.TaskOutput{
			{Kind: "image", URL: "https://storage.example/img.avif"},
			{Kind: "video", URL: "https://storage.example/skip.mp4"}, // 非图要过滤
		}
	})
	h := &V1Handler{service: s}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	h.respondImageSync(rec, req, taskID)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp V1ImageSyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].URL != "https://storage.example/img.avif" {
		t.Errorf("data 不对: %+v", resp.Data)
	}
	if resp.Created == 0 {
		t.Error("created 应有值")
	}
	// OpenAI 形状不该带任务字段
	if strings.Contains(rec.Body.String(), `"status"`) {
		t.Error("同步响应不该出现 status 字段(那是异步任务体的)")
	}
}

// TestRespondImageSyncFailed 失败 → 500 且带任务错误。
func TestRespondImageSyncFailed(t *testing.T) {
	s, taskID := syncTestServer(t)
	s.UpdateTask(taskID, func(task *service.Task) {
		task.Status = "failed"
		task.Error = "comfy exploded"
	})
	h := &V1Handler{service: s}
	rec := httptest.NewRecorder()

	h.respondImageSync(rec, httptest.NewRequest(http.MethodPost, "/x", nil), taskID)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "comfy exploded") {
		t.Fatalf("失败应 500 并带原因: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestRespondImageSyncWaitsForCompletion 排队中的任务要等到完成,不是立刻返回。
func TestRespondImageSyncWaitsForCompletion(t *testing.T) {
	s, taskID := syncTestServer(t)
	h := &V1Handler{service: s}
	rec := httptest.NewRecorder()

	go func() {
		time.Sleep(700 * time.Millisecond) // 跨过至少一个轮询周期
		s.UpdateTask(taskID, func(task *service.Task) {
			task.Status = "completed"
			task.Outputs = []service.TaskOutput{{Kind: "image", URL: "https://x/img.png"}}
		})
	}()
	start := time.Now()
	h.respondImageSync(rec, httptest.NewRequest(http.MethodPost, "/x", nil), taskID)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if time.Since(start) < 600*time.Millisecond {
		t.Error("应当等待任务完成,而不是拿着 queued 提前返回")
	}
}

// TestRespondImageSyncCallerHangup 调用方断开 → 轮询停止,不写响应。
func TestRespondImageSyncCallerHangup(t *testing.T) {
	s, taskID := syncTestServer(t)
	h := &V1Handler{service: s}
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/x", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() { h.respondImageSync(rec, req, taskID); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("调用方断开后轮询应立即退出")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("断开后不该写响应体: %s", rec.Body.String())
	}
}
