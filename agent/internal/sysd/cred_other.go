//go:build !linux

package sysd

import "os/exec"

func setCredential(*exec.Cmd, uint32, uint32) {}
