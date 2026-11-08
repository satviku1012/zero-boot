//go:build !linux

// isolation_other.go lets the node agent build and run on macOS for local
// development. There are no namespaces or cgroups here, so sandboxes run as
// plain child processes and every cgroup call fails, which makes the agent
// fall back to freezing with SIGSTOP/SIGCONT.
package main

import (
	"errors"
	"syscall"
)

var errNoCgroups = errors.New("cgroups v2 requires Linux")

// isolationAttr applies no namespaces off Linux.
func isolationAttr() *syscall.SysProcAttr { return nil }

func cgroupAttach(id string, pid int) error        { return errNoCgroups }
func cgroupSetFrozen(id string, frozen bool) error { return errNoCgroups }
func cgroupRemove(id string) error                 { return errNoCgroups }
