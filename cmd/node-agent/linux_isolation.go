//go:build linux

// linux_isolation.go holds everything Linux-specific about a sandbox:
//
//   - Namespaces: each sandbox gets its own PID and UTS (hostname) namespace.
//   - cgroups v2: each sandbox lives in /sys/fs/cgroup/zeroboot_<id>, and
//     writing "1" to its cgroup.freeze pauses every task in it at 0% CPU.
//
// cgroup.freeze is a core cgroup v2 file present in every non-root cgroup,
// so it works without enabling any controller in cgroup.subtree_control.
// We deliberately skip memory.max (see PROJECT.md §2): enabling the memory
// controller inside a container runs into the "no internal processes" rule.
//
// Non-Linux builds use isolation_other.go instead.
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// cgroupRoot is where the cgroup v2 unified hierarchy is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// isolationAttr returns the clone flags applied when the sandbox is spawned.
func isolationAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		// CLONE_NEWPID: the sandbox is PID 1 in a fresh PID namespace, so it
		//               cannot see or signal any other process on the node.
		// CLONE_NEWUTS: the sandbox gets its own hostname/domainname, so a
		//               sethostname inside it cannot rename the worker node.
		Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS,
	}
}

// cgroupPath is the sandbox's private cgroup directory.
func cgroupPath(id string) string {
	return filepath.Join(cgroupRoot, "zeroboot_"+id)
}

// cgroupAttach creates the sandbox's cgroup and moves its process into it.
// Any error (read-only /sys/fs/cgroup, cgroup v1 host, ...) tells the caller
// to fall back to SIGSTOP/SIGCONT.
func cgroupAttach(id string, pid int) error {
	dir := cgroupPath(id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	// Writing a PID to cgroup.procs migrates that process into the cgroup.
	if err := writeCgroupFile(dir, "cgroup.procs", strconv.Itoa(pid)); err != nil {
		os.Remove(dir)
		return err
	}
	return nil
}

// cgroupSetFrozen writes cgroup.freeze: "1" freezes the sandbox (0% CPU,
// memory kept intact), "0" thaws it so it resumes exactly where it stopped.
func cgroupSetFrozen(id string, frozen bool) error {
	value := "0"
	if frozen {
		value = "1"
	}
	return writeCgroupFile(cgroupPath(id), "cgroup.freeze", value)
}

// cgroupRemove deletes the sandbox's cgroup. The kernel only allows rmdir on
// a cgroup with no live tasks, so call this after the process is reaped.
func cgroupRemove(id string) error {
	return os.Remove(cgroupPath(id))
}

// writeCgroupFile writes a value to a cgroup interface file. These files
// already exist, so os.WriteFile's create/truncate flags are harmless.
func writeCgroupFile(dir, name, value string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644)
}
