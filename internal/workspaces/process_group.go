package workspaces

import (
	"bytes"
	"strconv"
)

func groupContainsOtherPID(output []byte, group int) bool {
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(string(fields[0]))
		if err != nil {
			continue
		}
		pgid, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			continue
		}
		if pgid == group && pid != group {
			return true
		}
	}
	return false
}
