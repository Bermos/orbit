package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Bermos/orbit/internal/pipeline"
)

func TestKubernetesPodIsLockedDown(t *testing.T) {
	k := Kubernetes{Client: fake.NewClientset(), Namespace: "ci", RuntimeClass: "gvisor", Memory: "2Gi"}
	spec := Spec{
		Assignment: pipeline.Assignment{JobID: "0b5f7c2e-1111-2222-3333-444455556666", Kind: "agent", App: "ledger", Commit: "abc"},
		CloneURL:   "http://orbit/git/ledger/api.git", GitAuth: "Authorization: Basic x", Image: "golang:1.26",
		Env: map[string]string{"CI": "true"}, Timeout: 30 * time.Minute,
	}
	pod := k.buildPod(spec)
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("pods must not get a service account token")
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "gvisor" || pod.Labels["orbit.dev/kind"] != "agent" {
		t.Errorf("pod = %+v", pod.ObjectMeta)
	}
	if len(pod.Spec.Containers) != 2 || pod.Spec.Containers[1].Name != gitContainer {
		t.Fatalf("agent jobs need the git container: %+v", pod.Spec.Containers)
	}
	job := pod.Spec.Containers[0]
	if sc := job.SecurityContext; *sc.AllowPrivilegeEscalation || *sc.Privileged || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("job security context = %+v", sc)
	}
	if job.Resources.Limits.Memory().String() != "2Gi" || job.Image != "golang:1.26" {
		t.Errorf("job container = %+v", job)
	}
	// The job token only reaches the clone container.
	for _, e := range job.Env {
		if strings.Contains(e.Value, "Basic") {
			t.Error("the job container must not see the git credentials")
		}
	}

	spec.Assignment.Kind = "script"
	if pod := k.buildPod(spec); len(pod.Spec.Containers) != 1 {
		t.Error("script jobs need no git container")
	}
}

func TestKubernetesReportsPodsThatCannotStart(t *testing.T) {
	client := fake.NewClientset()
	k := Kubernetes{Client: client, Namespace: "ci", StartTimeout: 3 * time.Second}
	go func() {
		// Play the kubelet: the image cannot be pulled.
		for range 50 {
			time.Sleep(50 * time.Millisecond)
			pods, _ := client.CoreV1().Pods("ci").List(context.Background(), metav1.ListOptions{})
			if len(pods.Items) == 0 {
				continue
			}
			p := pods.Items[0]
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "job", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "no such image"}}}}
			client.CoreV1().Pods("ci").UpdateStatus(context.Background(), &p, metav1.UpdateOptions{})
			return
		}
	}()
	var log bytes.Buffer
	_, err := k.Prepare(context.Background(), Spec{Assignment: pipeline.Assignment{JobID: "0b5f7c2e-1111-2222-3333-444455556666", Kind: "script"}, Image: "nope", Log: &log})
	if err == nil || !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Fatalf("err = %v", err)
	}
	if pods, _ := client.CoreV1().Pods("ci").List(context.Background(), metav1.ListOptions{}); len(pods.Items) != 0 {
		t.Error("the pod should be deleted")
	}
}

// TestSandboxTools runs the agent tools against a local workspace.
func TestSandboxTools(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	ws := &localWorkspace{dir: dir, env: os.Environ()}
	ctx := context.Background()
	call := func(tool string, in map[string]any) (string, error) {
		raw, _ := json.Marshal(in)
		var logs bytes.Buffer
		return runTool(ctx, ws, &logs, pipeline.ToolCall{ID: "1", Tool: tool, Input: raw})
	}
	if out, err := call("write_file", map[string]any{"path": "pkg/a.txt", "content": "one\ntwo\n"}); err != nil || !strings.Contains(out, "8 bytes") {
		t.Fatalf("write: %q %v", out, err)
	}
	if _, err := call("edit_file", map[string]any{"path": "pkg/a.txt", "old_text": "two", "new_text": "three"}); err != nil {
		t.Fatal(err)
	}
	if out, err := call("read_file", map[string]any{"path": "pkg/a.txt", "start_line": 2}); err != nil || out != "2\tthree\n" {
		t.Fatalf("read: %q %v", out, err)
	}
	if out, _ := call("list_files", map[string]any{}); !strings.Contains(out, "pkg/a.txt") || strings.Contains(out, ".git/") {
		t.Fatalf("list: %q", out)
	}
	if out, err := call("run", map[string]any{"command": "echo hi; exit 4"}); err != nil || !strings.HasPrefix(out, "exit code 4\nhi") {
		t.Fatalf("run: %q %v", out, err)
	}
	for _, p := range []string{"../x", "/etc/passwd", ".git/config"} {
		if _, err := call("read_file", map[string]any{"path": p}); err == nil {
			t.Errorf("read %s should be refused", p)
		}
	}
	if _, err := call("edit_file", map[string]any{"path": "main.go", "old_text": "nothing", "new_text": "x"}); err == nil {
		t.Error("edit with missing text should fail")
	}
}
