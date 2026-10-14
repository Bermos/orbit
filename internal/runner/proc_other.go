//go:build !unix

package runner

import "os/exec"

func killGroup(*exec.Cmd) {}
