//go:build darwin && cgo

package processhost

/*
#cgo LDFLAGS: -lproc
#include <errno.h>
#include <libproc.h>
#include <stdlib.h>
#include <string.h>
#include <sys/proc_info.h>
#include <sys/sysctl.h>

struct switchyard_process_info {
	int pid;
	int ppid;
	int pgid;
	int status;
	unsigned long long started_sec;
	unsigned long long started_usec;
	unsigned long long memory_bytes;
	unsigned long long user_time;
	unsigned long long system_time;
	char executable_path[PROC_PIDPATHINFO_MAXSIZE];
};

int switchyard_inspect_process(int pid, struct switchyard_process_info *result) {
	struct proc_taskallinfo task_info;
	memset(&task_info, 0, sizeof(task_info));
	int received = proc_pidinfo(pid, PROC_PIDTASKALLINFO, 0, &task_info, sizeof(task_info));
	if (received != sizeof(task_info)) {
		if (received == 0 && errno == 0) errno = ESRCH;
		return -1;
	}

	memset(result, 0, sizeof(*result));
	result->pid = task_info.pbsd.pbi_pid;
	result->ppid = task_info.pbsd.pbi_ppid;
	result->pgid = task_info.pbsd.pbi_pgid;
	result->status = task_info.pbsd.pbi_status;
	result->started_sec = task_info.pbsd.pbi_start_tvsec;
	result->started_usec = task_info.pbsd.pbi_start_tvusec;
	result->memory_bytes = task_info.ptinfo.pti_resident_size;
	result->user_time = task_info.ptinfo.pti_total_user;
	result->system_time = task_info.ptinfo.pti_total_system;
	if (proc_pidpath(pid, result->executable_path, sizeof(result->executable_path)) <= 0) {
		if (task_info.pbsd.pbi_status == SZOMB) return 0;
		return -1;
	}
	return 0;
}

int switchyard_process_status(int pid) {
	struct proc_bsdinfo process_info;
	memset(&process_info, 0, sizeof(process_info));
	int received = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &process_info, sizeof(process_info));
	if (received != sizeof(process_info)) return 0;
	return process_info.pbi_status;
}

int switchyard_list_group(int pgid, int *pids, int bytes) {
	return proc_listpgrppids(pgid, pids, bytes);
}

int switchyard_process_arguments(int pid, void *buffer, int bytes) {
	int query[3] = { CTL_KERN, KERN_PROCARGS2, pid };
	size_t size = (size_t)bytes;
	if (sysctl(query, 3, buffer, &size, NULL, 0) != 0) {
		return -1;
	}
	return (int)size;
}
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"syscall"
	"time"
	"unsafe"
)

const maximumProcessArgumentsBytes = 1024 * 1024
const maximumStableInspectionAttempts = 8

type systemInspector struct{}

func newSystemInspector() ProcessInspector {
	return systemInspector{}
}

func (systemInspector) Inspect(ctx context.Context, pid int) (ProcessSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ProcessSnapshot{}, err
	}
	if pid <= 1 {
		return ProcessSnapshot{}, ErrProcessNotFound
	}

	var lastErr error
	for attempt := 0; attempt < maximumStableInspectionAttempts; attempt++ {
		snapshot, retry, err := inspectProcessOnce(pid)
		if !retry {
			return snapshot, err
		}
		lastErr = err
		if err := waitForInspectionRetry(ctx); err != nil {
			return ProcessSnapshot{}, err
		}
	}
	return ProcessSnapshot{}, fmt.Errorf("%w: inspect pid %d: %v", ErrUnstableGroup, pid, lastErr)
}

func inspectProcessOnce(pid int) (ProcessSnapshot, bool, error) {
	before, beforePath, err := inspectProcessMetadata(pid)
	if err != nil {
		return ProcessSnapshot{}, inspectionCanRetry(err), err
	}
	if before.Status == "zombie" {
		return before, false, nil
	}

	arguments, err := inspectProcessArguments(pid)
	if err != nil {
		return ProcessSnapshot{}, inspectionCanRetry(err), err
	}

	after, afterPath, err := inspectProcessMetadata(pid)
	if err != nil {
		return ProcessSnapshot{}, inspectionCanRetry(err), err
	}
	if after.Status == "zombie" {
		return after, false, nil
	}
	if !sameInspection(before.Identity, beforePath, after.Identity, afterPath) {
		return ProcessSnapshot{}, true, ErrUnstableGroup
	}
	after.Identity.CommandFingerprint = fingerprintCommand(afterPath, arguments)
	return after, false, nil
}

func inspectProcessMetadata(pid int) (ProcessSnapshot, string, error) {
	var processInfo C.struct_switchyard_process_info
	result, callErr := C.switchyard_inspect_process(C.int(pid), &processInfo)
	if result != 0 {
		return ProcessSnapshot{}, "", processInspectionError(pid, callErr)
	}
	snapshot := ProcessSnapshot{
		Identity: ProcessIdentity{
			PID:            int(processInfo.pid),
			ParentPID:      int(processInfo.ppid),
			ProcessGroupID: int(processInfo.pgid),
			StartedAt:      time.Unix(int64(processInfo.started_sec), int64(processInfo.started_usec)*int64(time.Microsecond)).UTC(),
		},
		Status:      processStatus(int(processInfo.status)),
		MemoryBytes: uint64(processInfo.memory_bytes),
		CPUTime:     time.Duration(uint64(processInfo.user_time) + uint64(processInfo.system_time)),
	}
	executablePath := C.GoString(&processInfo.executable_path[0])
	return snapshot, executablePath, nil
}

func sameInspection(
	before ProcessIdentity,
	beforePath string,
	after ProcessIdentity,
	afterPath string,
) bool {
	return before.PID == after.PID &&
		before.ParentPID == after.ParentPID &&
		before.ProcessGroupID == after.ProcessGroupID &&
		before.StartedAt.Equal(after.StartedAt) &&
		beforePath == afterPath
}

func inspectionCanRetry(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.EINTR)
}

func waitForInspectionRetry(ctx context.Context) error {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (inspector systemInspector) ListGroup(ctx context.Context, processGroupID int) ([]ProcessSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if processGroupID <= 1 {
		return nil, errors.New("process group id must be greater than one")
	}

	// Unlike proc_listpids, proc_listpgrppids does not provide a useful sizing
	// result for a nil buffer on all supported Darwin releases. Start with a
	// bounded buffer and grow it whenever the kernel fills the buffer.
	processCapacity := 256
	for attempts := 0; attempts < 8; attempts++ {
		processIDs := make([]C.int, processCapacity)
		processCount, callErr := C.switchyard_list_group(
			C.int(processGroupID),
			&processIDs[0],
			C.int(len(processIDs)*int(C.sizeof_int)),
		)
		if processCount < 0 {
			return nil, fmt.Errorf("list process group %d: %w", processGroupID, callErr)
		}
		if int(processCount) >= len(processIDs) {
			processCapacity *= 2
			continue
		}

		snapshots := make([]ProcessSnapshot, 0, int(processCount))
		for _, processID := range processIDs[:int(processCount)] {
			if processID <= 1 {
				continue
			}
			snapshot, err := inspector.Inspect(ctx, int(processID))
			if errors.Is(err, ErrProcessNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if snapshot.Status == "zombie" {
				continue
			}
			if snapshot.Identity.ProcessGroupID == processGroupID {
				snapshots = append(snapshots, snapshot)
			}
		}
		sort.Slice(snapshots, func(left, right int) bool {
			return snapshots[left].Identity.PID < snapshots[right].Identity.PID
		})
		return snapshots, nil
	}
	return nil, ErrUnstableGroup
}

func inspectProcessArguments(pid int) ([]string, error) {
	buffer := make([]byte, maximumProcessArgumentsBytes)
	received, callErr := C.switchyard_process_arguments(C.int(pid), unsafe.Pointer(&buffer[0]), C.int(len(buffer)))
	if received < 0 {
		return nil, processInspectionError(pid, callErr)
	}
	buffer = buffer[:int(received)]
	if len(buffer) < 4 {
		return nil, fmt.Errorf("inspect process %d arguments: malformed response", pid)
	}
	argumentCount := int(int32(binary.LittleEndian.Uint32(buffer[:4])))
	if argumentCount < 1 || argumentCount > 65536 {
		return nil, fmt.Errorf("inspect process %d arguments: invalid argument count", pid)
	}

	offset := 4
	_, offset, ok := readNullTerminated(buffer, offset)
	if !ok {
		return nil, fmt.Errorf("inspect process %d arguments: missing executable", pid)
	}
	for offset < len(buffer) && buffer[offset] == 0 {
		offset++
	}

	arguments := make([]string, 0, argumentCount)
	for len(arguments) < argumentCount {
		argument, nextOffset, ok := readNullTerminated(buffer, offset)
		if !ok {
			return nil, fmt.Errorf("inspect process %d arguments: missing argument %d", pid, len(arguments))
		}
		arguments = append(arguments, argument)
		offset = nextOffset
	}
	return arguments, nil
}

func readNullTerminated(contents []byte, offset int) (string, int, bool) {
	if offset >= len(contents) {
		return "", offset, false
	}
	remaining := contents[offset:]
	end := bytes.IndexByte(remaining, 0)
	if end < 0 {
		return "", offset, false
	}
	return string(remaining[:end]), offset + end + 1, true
}

func processInspectionError(pid int, callErr error) error {
	if errors.Is(callErr, syscall.ESRCH) || errors.Is(callErr, syscall.ENOENT) {
		return fmt.Errorf("%w: pid %d", ErrProcessNotFound, pid)
	}
	if errors.Is(callErr, syscall.EINVAL) {
		status := C.switchyard_process_status(C.int(pid))
		if status == 0 || status == C.SZOMB {
			return fmt.Errorf("%w: pid %d", ErrProcessNotFound, pid)
		}
	}
	return fmt.Errorf("inspect process %d: %w", pid, callErr)
}

func processStatus(status int) string {
	switch status {
	case int(C.SZOMB):
		return "zombie"
	case int(C.SSTOP):
		return "stopped"
	default:
		return "running"
	}
}
