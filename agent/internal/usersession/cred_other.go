//go:build !linux

package usersession

import "os/exec"

func setCredential(*exec.Cmd, uint32, uint32) {}
