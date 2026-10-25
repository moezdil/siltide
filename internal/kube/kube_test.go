package kube

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogDir(t *testing.T) {
	p, ok := ParseLogDir("ml_chat-api-7d9f8b6c5-x2k9p_0a1b2c3d-1111-2222-3333-444455556666")
	if !ok || p.Namespace != "ml" || p.Name != "chat-api-7d9f8b6c5-x2k9p" || p.Workload != "Deployment/chat-api" {
		t.Fatalf("got %+v %v", p, ok)
	}
	if _, ok := ParseLogDir("not-a-pod-dir"); ok {
		t.Fatal("junk accepted")
	}
	for name, want := range map[string]string{
		"web-0":                  "StatefulSet/web",
		"notebook-alice-0":       "StatefulSet/notebook-alice",
		"train-job-abc12":        "ReplicaSet/train-job",
		"standalone":             "Pod/standalone",
		"embed-svc-6c7d9f5b8-p1": "Pod/embed-svc-6c7d9f5b8-p1", // 2-char suffix is not a hash
	} {
		if got := guessWorkload(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

func TestLogsFromNode(t *testing.T) {
	dir := t.TempDir()
	pod := filepath.Join(dir, "ml_train-0_0a1b2c3d-1111-2222-3333-444455556666", "main")
	if err := os.MkdirAll(pod, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "2026-09-16T10:00:00.000000000Z stdout F step "+string(rune('a'+i)))
	}
	if err := os.WriteFile(filepath.Join(pod, "0.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{logDir: dir, pods: map[string]Pod{}, enabled: true}
	out, err := r.Logs(context.Background(), "ml", "train-0", "", 3)
	if err != nil || out != "step r\nstep s\nstep t" {
		t.Fatalf("%q %v", out, err)
	}
	desc, err := r.Describe(context.Background(), "ml", "train-0")
	if err != nil || !strings.Contains(desc, "Workload:   StatefulSet/train") {
		t.Fatalf("%q %v", desc, err)
	}
}

func TestAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/pods"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{
				"metadata": map[string]any{"name": "chat-api-7d9f8b6c5-x2k9p", "namespace": "inf", "uid": "u1",
					"ownerReferences": []any{map[string]any{"kind": "ReplicaSet", "name": "chat-api-7d9f8b6c5"}}},
				"spec": map[string]any{"nodeName": "n1", "containers": []any{map[string]any{"name": "main",
					"resources": map[string]any{"requests": map[string]any{"nvidia.com/gpu": "2"}}}}},
				"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "main", "containerID": "containerd://abc", "ready": true}}},
			}}})
		case strings.Contains(r.URL.Path, "/log"):
			_, _ = w.Write([]byte("line1\nline2\n"))
		case strings.Contains(r.URL.Path, "/events"):
			if !strings.Contains(r.URL.Query().Get("fieldSelector"), "involvedObject.uid=u1") {
				w.WriteHeader(http.StatusBadRequest) // events of a pod with the same name but another uid
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"type": "Normal", "reason": "Scheduled", "message": "ok", "eventTime": "2026-09-16T10:00:05Z"}}})
		}
	}))
	defer srv.Close()
	kc := "current-context: c\ncontexts:\n- name: c\n  context: {cluster: k, user: u}\nclusters:\n- name: k\n  cluster: {server: " + srv.URL + "}\nusers:\n- name: u\n  user: {token: tok}\n"
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{logDir: filepath.Join(t.TempDir(), "none"), pods: map[string]Pod{}}
	if err := r.fromKubeconfig(path, ""); err != nil {
		t.Fatal(err)
	}
	p, ok := r.Lookup("u1")
	if !ok || p.Workload != "Deployment/chat-api" || p.ContainerName("abc") != "main" || p.Requests["nvidia.com/gpu"] != "2" || p.Ready != "1/1" {
		t.Fatalf("%+v %v", p, ok)
	}
	desc, err := r.Describe(context.Background(), "inf", "chat-api-7d9f8b6c5-x2k9p")
	if err != nil || !strings.Contains(desc, "Scheduled") || !strings.Contains(desc, "nvidia.com/gpu=2") {
		t.Fatalf("%q %v", desc, err)
	}
	if !strings.Contains(desc, "10:00:05 Normal") {
		t.Fatalf("event time not taken from eventTime: %q", desc)
	}
	logs, err := r.Logs(context.Background(), "inf", "chat-api-7d9f8b6c5-x2k9p", "main", 10)
	if err != nil || !strings.HasPrefix(logs, "line1") {
		t.Fatalf("%q %v", logs, err)
	}
}

func TestPodResources(t *testing.T) {
	// One pod, one container, 2 device IDs.
	devs := field(1, []byte("nvidia.com/gpu"))
	devs = append(devs, field(2, []byte("GPU-aaa"))...)
	devs = append(devs, field(2, []byte("GPU-bbb"))...)
	container := append(field(1, []byte("main")), field(2, devs)...)
	pod := append(field(1, []byte("train-0")), field(2, []byte("ml"))...)
	pod = append(pod, field(3, container)...)
	msg := field(1, pod)

	got, err := ParseListResponse(msg)
	if err != nil || len(got) != 2 || got[1].DeviceID != "GPU-bbb" || got[0].Pod != "train-0" || got[0].Namespace != "ml" || got[0].Container != "main" {
		t.Fatalf("%+v %v", got, err)
	}

	// End to end over a unix socket speaking h2c (HTTP/2 without TLS) like the kubelet.
	sock := filepath.Join(t.TempDir(), "k.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.PodResourcesLister/List" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		frame := append([]byte{0, 0, 0, 0, 0}, msg...)
		binary.BigEndian.PutUint32(frame[1:5], uint32(len(msg)))
		_, _ = w.Write(frame)
		w.Header().Set("Grpc-Status", "0")
	})
	s := &http.Server{Handler: h, Protocols: new(http.Protocols)}
	s.Protocols.SetHTTP1(true)
	s.Protocols.SetUnencryptedHTTP2(true)
	go func() { _ = s.Serve(ln) }()
	defer func() { _ = s.Close() }()

	pr := newPodResources(sock)
	allocs, err := pr.Allocations(context.Background())
	if err != nil || len(allocs) != 2 {
		t.Fatalf("%+v %v", allocs, err)
	}
}

// TestAttemptsRecordWhatWasTried covers the account the Kubernetes tab and
// --diagnose give when no pod source is found: every place that was looked
// at, and why it did not answer.
func TestAttemptsRecordWhatWasTried(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SILTIDE_POD_LOG_DIR", filepath.Join(dir, "no-such-pods"))
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	missing := filepath.Join(dir, "no-such-kubeconfig")

	r := New(Options{Kubeconfig: missing})
	if r.Enabled() {
		t.Fatalf("nothing should have been detected, source %q", r.Source())
	}
	got := r.Attempts()
	if len(got) != 3 {
		t.Fatalf("recorded %d attempts, want the log directory, the service account and the kubeconfig: %+v", len(got), got)
	}
	for _, a := range got {
		if a.What == "" || a.Where == "" {
			t.Errorf("an attempt says nothing useful: %+v", a)
		}
		if a.Err == "" {
			t.Errorf("attempt %q on %q claims to have worked", a.What, a.Where)
		}
	}
	if !strings.Contains(got[0].Where, "no-such-pods") || !strings.Contains(got[2].Where, "no-such-kubeconfig") {
		t.Errorf("attempts name the wrong paths: %+v", got)
	}
	if !strings.Contains(got[1].Err, "not running in a pod") {
		t.Errorf("the service account attempt should say why: %q", got[1].Err)
	}

	// a directory that exists is recorded as the source that answered
	logs := filepath.Join(dir, "pods")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SILTIDE_POD_LOG_DIR", logs)
	r = New(Options{Kubeconfig: missing})
	if !r.Enabled() || r.Source() != "log-dir" {
		t.Fatalf("enabled %v source %q", r.Enabled(), r.Source())
	}
	if a := r.Attempts()[0]; a.Err != "" {
		t.Errorf("the attempt that worked carries an error: %+v", a)
	}
}
