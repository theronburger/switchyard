package containerhost

import (
	"errors"
	"fmt"
	"sort"
)

func canonicalPortBindings(bindings []PortBinding) ([]PortBinding, error) {
	canonical := clonePortBindings(bindings)
	seenHostPorts := make(map[int]struct{}, len(canonical))
	for _, binding := range canonical {
		if binding.Host != LoopbackHostIPv4 {
			return nil, errors.New("container host binding must use literal IPv4 loopback")
		}
		if !validPort(binding.HostPort) || !validPort(binding.ContainerPort) {
			return nil, errors.New("container port binding is outside the valid port range")
		}
		if binding.Protocol != PortProtocolTCP {
			return nil, errors.New("container port binding protocol is unsupported")
		}
		if _, duplicate := seenHostPorts[binding.HostPort]; duplicate {
			return nil, errors.New("container host port binding is duplicated")
		}
		seenHostPorts[binding.HostPort] = struct{}{}
	}
	sortPortBindings(canonical)
	return canonical, nil
}

func normalizeObservedPortBindings(bindings []PortBinding) []PortBinding {
	normalized := clonePortBindings(bindings)
	sortPortBindings(normalized)
	return normalized
}

func clonePortBindings(bindings []PortBinding) []PortBinding {
	if bindings == nil {
		return nil
	}
	return append([]PortBinding(nil), bindings...)
}

func sortPortBindings(bindings []PortBinding) {
	sort.Slice(bindings, func(left, right int) bool {
		if bindings[left].ContainerPort != bindings[right].ContainerPort {
			return bindings[left].ContainerPort < bindings[right].ContainerPort
		}
		if bindings[left].Protocol != bindings[right].Protocol {
			return bindings[left].Protocol < bindings[right].Protocol
		}
		if bindings[left].Host != bindings[right].Host {
			return bindings[left].Host < bindings[right].Host
		}
		return bindings[left].HostPort < bindings[right].HostPort
	})
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

func portBindingsEqual(left, right []PortBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func publishArgument(binding PortBinding) string {
	return fmt.Sprintf("%s:%d:%d/%s", binding.Host, binding.HostPort, binding.ContainerPort, binding.Protocol)
}
