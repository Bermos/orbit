// Command orbit-runner takes jobs from an Orbit server and runs them, as
// Kubernetes pods or as local processes.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Bermos/orbit/internal/runner"
)

// version is set at build time.
var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("runner stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	concurrency, err := strconv.Atoi(env("ORBIT_RUNNER_CONCURRENCY", "2"))
	if err != nil {
		return errors.New("ORBIT_RUNNER_CONCURRENCY must be a number")
	}
	cfg := runner.Config{
		URL:         os.Getenv("ORBIT_URL"),
		Token:       os.Getenv("ORBIT_RUNNER_TOKEN"),
		Concurrency: concurrency,
		AgentImage:  os.Getenv("ORBIT_RUNNER_AGENT_IMAGE"),
		Version:     version,
	}
	switch executor := env("ORBIT_RUNNER_EXECUTOR", "kubernetes"); executor {
	case "kubernetes":
		config, err := kubeConfig()
		if err != nil {
			return err
		}
		client, err := kubernetes.NewForConfig(config)
		if err != nil {
			return err
		}
		cfg.Executor = runner.Kubernetes{
			Client:       client,
			Config:       config,
			Namespace:    env("ORBIT_RUNNER_NAMESPACE", namespace()),
			GitImage:     os.Getenv("ORBIT_RUNNER_GIT_IMAGE"),
			RuntimeClass: os.Getenv("ORBIT_RUNNER_RUNTIME_CLASS"),
			CPU:          os.Getenv("ORBIT_RUNNER_CPU_LIMIT"),
			Memory:       os.Getenv("ORBIT_RUNNER_MEMORY_LIMIT"),
		}
	case "local":
		slog.Warn("the local executor runs jobs on this machine without isolation; use it for development only")
		cfg.Executor = runner.Local{Dir: os.Getenv("ORBIT_RUNNER_WORK_DIR")}
	default:
		return errors.New("ORBIT_RUNNER_EXECUTOR must be kubernetes or local, not " + executor)
	}
	return runner.Run(ctx, cfg)
}

// kubeConfig uses the in-cluster configuration, or KUBECONFIG (or
// ~/.kube/config) outside a cluster.
func kubeConfig() (*rest.Config, error) {
	if c, err := rest.InClusterConfig(); err == nil {
		return c, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

// namespace is the runner's own namespace when it runs in a cluster.
func namespace() string {
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return string(b)
	}
	return "default"
}
