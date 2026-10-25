// Package rootless runs regview in a storage-compatible user namespace.
package rootless

import (
	"context"
	"fmt"
	"io"
	"os/user"
	"strconv"
	"syscall"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"go.podman.io/storage/pkg/unshare"
)

// Run returns started=false only if namespace setup failed before the child
// could run. Unlike MaybeReexecUsingUserNamespace it does not exit the parent,
// allowing automatic discovery to continue with Docker when setup is unavailable.
// The storage helper preserves the original UID/GID in the child's environment.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (started bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	uid, gid := unshare.GetRootlessUID(), unshare.GetRootlessGID()
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return false, fmt.Errorf("look up rootless user: %w", err)
	}
	uids, gids, err := unshare.GetSubIDMappings(account.Username, account.Username)
	if err != nil {
		return false, fmt.Errorf("read subordinate IDs: %w", err)
	}
	cmd := unshare.Command(args...)
	cmd.UnshareFlags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS
	cmd.UseNewuidmap, cmd.UseNewgidmap = true, true
	cmd.UidMappings, cmd.GidMappings = mappings(uid, uids), mappings(gid, gids)
	cmd.GidMappingsEnableSetgroups = true
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("enter rootless storage namespace: %w", err)
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = cmd.Process.Kill()
		}
	})
	err = cmd.Wait()
	close(done)
	stop()
	return true, err
}

func mappings(id int, subordinate []specs.LinuxIDMapping) []specs.LinuxIDMapping {
	result := make([]specs.LinuxIDMapping, 1, len(subordinate)+1)
	result[0] = specs.LinuxIDMapping{ContainerID: 0, HostID: uint32(id), Size: 1}
	next := uint32(1)
	for _, mapping := range subordinate {
		mapping.ContainerID = next
		result = append(result, mapping)
		next += mapping.Size
	}
	return result
}
