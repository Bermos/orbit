package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// Kubernetes runs each job as a pod, the way Woodpecker's Kubernetes backend
// runs steps: an init container clones the repository into a shared
// emptyDir, then the job's container idles while the runner executes the
// script (or an agent's tool calls) in it through the exec API. Agent jobs get
// a second container with git, which the runner uses to collect the changes.
//
// Pods run without a service account token, without privilege escalation
// and with all capabilities dropped. A runtime class (gVisor, Kata) adds a
// stronger sandbox where the cluster has one. Agent pods carry the label
// orbit.dev/kind=agent so a NetworkPolicy can limit what they reach.
type Kubernetes struct {
	Client    kubernetes.Interface
	Config    *rest.Config
	Namespace string
	// GitImage clones repositories and collects agent changes.
	GitImage string
	// RuntimeClass, if set, is the pods' runtimeClassName.
	RuntimeClass string
	// CPU and Memory are the job container's limits ("2", "4Gi"); empty
	// means none.
	CPU, Memory string
	// StartTimeout bounds pulling images and cloning.
	StartTimeout time.Duration
}

// DefaultGitImage clones repositories in pods.
const DefaultGitImage = "alpine/git:2.54.0"

const (
	workspaceDir = "/workspace"
	checkoutDir  = "/workspace/src"
	jobContainer = "job"
	gitContainer = "git"
)

func (Kubernetes) Name() string { return "kubernetes" }

// idle keeps a container running until the runner deletes the pod.
var idle = []string{"sh", "-c", "trap 'exit 0' TERM; while :; do sleep 3600 & wait $!; done"}

// cloneScript runs in the init container. The checkout is made writable for
// whatever user the job's image runs as.
const cloneScript = `set -e
git -c "http.extraHeader=$ORBIT_GIT_AUTH" clone --quiet --no-checkout "$ORBIT_CLONE_URL" ` + checkoutDir + `
git -C ` + checkoutDir + ` -c advice.detachedHead=false checkout --quiet --detach "$ORBIT_COMMIT"
chmod -R a+rwX ` + checkoutDir + `
echo "Checked out $ORBIT_COMMIT"`

// buildPod describes the pod for a job.
func (k Kubernetes) buildPod(spec Spec) *corev1.Pod {
	a := spec.Assignment
	suffix := make([]byte, 3)
	rand.Read(suffix)
	name := fmt.Sprintf("orbit-%s-%s", strings.ToLower(strings.ReplaceAll(a.JobID, "-", "")[:12]), hex.EncodeToString(suffix))

	var env []corev1.EnvVar
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, corev1.EnvVar{Name: key, Value: spec.Env[key]})
	}
	no := false
	secure := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &no,
		Privileged:               &no,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	// Git needs to chown and chmod the checkout it creates.
	gitSecure := secure.DeepCopy()
	gitSecure.Capabilities.Add = []corev1.Capability{"CHOWN", "FOWNER", "DAC_OVERRIDE"}
	mounts := []corev1.VolumeMount{{Name: "workspace", MountPath: workspaceDir}}

	job := corev1.Container{
		Name: jobContainer, Image: spec.Image, Command: idle, WorkingDir: checkoutDir, Env: env,
		VolumeMounts: mounts, SecurityContext: secure,
	}
	if k.CPU != "" || k.Memory != "" {
		job.Resources.Limits = corev1.ResourceList{}
		if q, err := resource.ParseQuantity(k.CPU); err == nil && k.CPU != "" {
			job.Resources.Limits[corev1.ResourceCPU] = q
		}
		if q, err := resource.ParseQuantity(k.Memory); err == nil && k.Memory != "" {
			job.Resources.Limits[corev1.ResourceMemory] = q
		}
	}
	containers := []corev1.Container{job}
	if a.Kind == "agent" {
		containers = append(containers, corev1.Container{
			Name: gitContainer, Image: k.gitImage(), Command: idle, WorkingDir: checkoutDir,
			VolumeMounts: mounts, SecurityContext: gitSecure,
		})
	}
	deadline := int64((spec.Timeout + 10*time.Minute) / time.Second)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "orbit-runner",
				"orbit.dev/job":                a.JobID,
				"orbit.dev/kind":               a.Kind,
				"orbit.dev/app":                a.App,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &no,
			EnableServiceLinks:           &no,
			ActiveDeadlineSeconds:        &deadline,
			Volumes:                      []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			InitContainers: []corev1.Container{{
				Name: "clone", Image: k.gitImage(), Command: []string{"sh", "-c", cloneScript},
				Env: []corev1.EnvVar{
					{Name: "ORBIT_CLONE_URL", Value: spec.CloneURL},
					{Name: "ORBIT_GIT_AUTH", Value: spec.GitAuth},
					{Name: "ORBIT_COMMIT", Value: a.Commit},
					{Name: "GIT_TERMINAL_PROMPT", Value: "0"},
				},
				VolumeMounts: mounts, SecurityContext: gitSecure,
			}},
			Containers: containers,
		},
	}
	if k.RuntimeClass != "" {
		pod.Spec.RuntimeClassName = &k.RuntimeClass
	}
	return pod
}

func (k Kubernetes) gitImage() string {
	if k.GitImage != "" {
		return k.GitImage
	}
	return DefaultGitImage
}

func (k Kubernetes) Prepare(ctx context.Context, spec Spec) (Workspace, error) {
	pods := k.Client.CoreV1().Pods(k.Namespace)
	pod, err := pods.Create(ctx, k.buildPod(spec), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create pod: %w", err)
	}
	ws := &podWorkspace{k: k, name: pod.Name}
	fmt.Fprintf(spec.Log, "Started pod %s/%s\n", k.Namespace, pod.Name)
	if err := ws.waitRunning(ctx, spec.Log); err != nil {
		ws.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return ws, nil
}

type podWorkspace struct {
	k    Kubernetes
	name string
}

// waitRunning waits until the clone has finished and the job's containers
// run, and copies the clone's output into the job log.
func (w *podWorkspace) waitRunning(ctx context.Context, log io.Writer) error {
	timeout := w.k.StartTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pods := w.k.Client.CoreV1().Pods(w.k.Namespace)
	for {
		pod, err := pods.Get(ctx, w.name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("pod: %w", err)
		}
		if err := podProblem(pod); err != nil {
			w.copyLogs(ctx, "clone", log)
			return err
		}
		if pod.Status.Phase == corev1.PodRunning && allRunning(pod) {
			w.copyLogs(ctx, "clone", log)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the pod did not start in time (%s)", podState(pod))
		case <-time.After(time.Second):
		}
	}
}

func allRunning(pod *corev1.Pod) bool {
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, s := range pod.Status.ContainerStatuses {
		if s.State.Running == nil {
			return false
		}
	}
	return true
}

// podProblem reports a pod that cannot start: a failed clone, an image that
// cannot be pulled, a container that exited.
func podProblem(pod *corev1.Pod) error {
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return fmt.Errorf("the pod stopped: %s", podState(pod))
	}
	for _, s := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		if t := s.State.Terminated; t != nil && t.ExitCode != 0 {
			return fmt.Errorf("container %s exited with code %d (%s)", s.Name, t.ExitCode, t.Reason)
		}
		if wt := s.State.Waiting; wt != nil {
			switch wt.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
				return fmt.Errorf("container %s cannot start: %s: %s", s.Name, wt.Reason, wt.Message)
			}
		}
	}
	return nil
}

func podState(pod *corev1.Pod) string {
	state := string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		state += ": " + pod.Status.Reason
	}
	return state
}

func (w *podWorkspace) copyLogs(ctx context.Context, container string, log io.Writer) {
	stream, err := w.k.Client.CoreV1().Pods(w.k.Namespace).GetLogs(w.name, &corev1.PodLogOptions{Container: container}).Stream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()
	io.Copy(log, io.LimitReader(stream, 1<<20))
}

func (w *podWorkspace) Exec(ctx context.Context, c Command) (int, error) {
	if w.k.Config == nil {
		return -1, errors.New("no Kubernetes client configuration for exec")
	}
	container := jobContainer
	if c.Helper {
		container = gitContainer
	}
	// The exec API has no working directory.
	argv := append([]string{"sh", "-c", `cd ` + checkoutDir + ` && exec "$@"`, "sh"}, c.Argv...)
	req := w.k.Client.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(w.k.Namespace).Name(w.name).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   argv,
			Stdin:     c.Stdin != nil,
			Stdout:    c.Stdout != nil,
			Stderr:    c.Stderr != nil,
		}, scheme.ParameterCodec)
	spdy, err := remotecommand.NewSPDYExecutor(w.k.Config, "POST", req.URL())
	if err != nil {
		return -1, err
	}
	ws, err := remotecommand.NewWebSocketExecutor(w.k.Config, "GET", req.URL().String())
	if err != nil {
		return -1, err
	}
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return -1, err
	}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr})
	var exitErr utilexec.ExitError
	if errors.As(err, &exitErr) && exitErr.Exited() {
		return exitErr.ExitStatus(), nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, err
	}
	return 0, nil
}

func (w *podWorkspace) Close(ctx context.Context) error {
	grace := int64(0)
	return w.k.Client.CoreV1().Pods(w.k.Namespace).Delete(ctx, w.name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
}
