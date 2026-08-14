package marketplace

import (
	"fmt"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	serverlessProjectionOwner             = "switchyard.marketplace.serverless.v1"
	serverlessProjectionOwnerHeaderPrefix = "// switchyard-owner: "
	serverlessProjectionHashHeaderPrefix  = "// switchyard-payload-sha256: "
)

type OwnedServerlessProjection struct {
	RelativePath  string
	Contents      []byte
	PayloadSHA256 string
	ContentSHA256 string
}

func RenderServerlessProjection(
	overlay ServerlessOverlay,
	ports []PortAssignment,
) (OwnedServerlessProjection, error) {
	if err := validateServerlessOverlayLocation(overlay); err != nil {
		return OwnedServerlessProjection{}, err
	}
	if len(overlay.Overrides) == 0 {
		return OwnedServerlessProjection{}, fmt.Errorf("render serverless projection: at least one override is required")
	}

	portsByRequirement := make(map[string]PortAssignment, len(ports))
	for _, assignment := range ports {
		if assignment.RequirementID == "" {
			return OwnedServerlessProjection{}, fmt.Errorf("render serverless projection: port requirement ID is empty")
		}
		if assignment.Port < 1 || assignment.Port > 65535 {
			return OwnedServerlessProjection{}, fmt.Errorf(
				"render serverless projection: port for requirement %q is outside 1-65535",
				assignment.RequirementID,
			)
		}
		if _, duplicate := portsByRequirement[assignment.RequirementID]; duplicate {
			return OwnedServerlessProjection{}, fmt.Errorf(
				"render serverless projection: duplicate port requirement %q",
				assignment.RequirementID,
			)
		}
		portsByRequirement[assignment.RequirementID] = assignment
	}

	overrides := append([]ServerlessOverride(nil), overlay.Overrides...)
	sort.Slice(overrides, func(left, right int) bool {
		return configurationAccess(overrides[left].ConfigurationPath) <
			configurationAccess(overrides[right].ConfigurationPath)
	})

	initializers := make(map[string]struct{})
	assignments := make([]string, 0, len(overrides))
	seenPaths := make(map[string]struct{}, len(overrides))
	for _, override := range overrides {
		if err := validateConfigurationPath(override.ConfigurationPath); err != nil {
			return OwnedServerlessProjection{}, fmt.Errorf("render serverless projection: %w", err)
		}
		access := configurationAccess(override.ConfigurationPath)
		if _, duplicate := seenPaths[access]; duplicate {
			return OwnedServerlessProjection{}, fmt.Errorf(
				"render serverless projection: duplicate configuration path %s",
				access,
			)
		}
		seenPaths[access] = struct{}{}

		port, exists := portsByRequirement[override.PortRequirement]
		if !exists {
			return OwnedServerlessProjection{}, fmt.Errorf(
				"render serverless projection: port requirement %q is not assigned",
				override.PortRequirement,
			)
		}
		value, err := renderOverlayValue(override.Format, port)
		if err != nil {
			return OwnedServerlessProjection{}, fmt.Errorf(
				"render serverless projection: configuration path %s: %w",
				access,
				err,
			)
		}
		for segmentCount := 1; segmentCount < len(override.ConfigurationPath); segmentCount++ {
			initializers[configurationAccess(override.ConfigurationPath[:segmentCount])] = struct{}{}
		}
		assignments = append(assignments, access+" = "+value)
	}

	initializerLines := make([]string, 0, len(initializers))
	for access := range initializers {
		initializerLines = append(initializerLines, access+" ??= {}")
	}
	sort.Strings(initializerLines)
	sort.Strings(assignments)

	var payload strings.Builder
	payload.WriteString("const configuration = require(")
	payload.WriteString(strconv.Quote("./" + overlay.SourceConfig))
	payload.WriteString(")\n\n")
	for _, line := range initializerLines {
		payload.WriteString(line)
		payload.WriteByte('\n')
	}
	payload.WriteByte('\n')
	for _, line := range assignments {
		payload.WriteString(line)
		payload.WriteByte('\n')
	}
	payload.WriteString("\nmodule.exports = configuration\n")

	payloadContents := []byte(payload.String())
	payloadHash := contentSHA256(payloadContents)
	contents := []byte(
		serverlessProjectionOwnerHeaderPrefix + serverlessProjectionOwner + "\n" +
			serverlessProjectionHashHeaderPrefix + payloadHash + "\n\n",
	)
	contents = append(contents, payloadContents...)
	return OwnedServerlessProjection{
		RelativePath:  path.Join(overlay.Directory, overlay.Filename),
		Contents:      contents,
		PayloadSHA256: payloadHash,
		ContentSHA256: contentSHA256(contents),
	}, nil
}

func validateServerlessOverlayLocation(overlay ServerlessOverlay) error {
	if overlay.Directory == "" || path.IsAbs(overlay.Directory) ||
		path.Clean(overlay.Directory) != overlay.Directory ||
		strings.HasPrefix(overlay.Directory, "../") ||
		strings.ContainsRune(overlay.Directory, '\\') {
		return fmt.Errorf("render serverless projection: directory must be a clean relative path")
	}
	if !isSingleRelativePathSegment(overlay.Filename) {
		return fmt.Errorf("render serverless projection: filename must be one relative path segment")
	}
	if !isSingleRelativePathSegment(overlay.SourceConfig) {
		return fmt.Errorf("render serverless projection: source config must be one relative path segment")
	}
	if overlay.Filename == overlay.SourceConfig {
		return fmt.Errorf("render serverless projection: projection cannot replace its source config")
	}
	return nil
}

func isSingleRelativePathSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		path.Base(value) == value && !strings.ContainsRune(value, '\\') &&
		!strings.ContainsRune(value, 0)
}

func validateConfigurationPath(configurationPath []string) error {
	if len(configurationPath) == 0 {
		return fmt.Errorf("configuration path is empty")
	}
	for _, segment := range configurationPath {
		if segment == "" || strings.ContainsRune(segment, 0) {
			return fmt.Errorf("configuration path contains an empty or NUL segment")
		}
	}
	return nil
}

func configurationAccess(configurationPath []string) string {
	var access strings.Builder
	access.WriteString("configuration")
	for _, segment := range configurationPath {
		access.WriteByte('[')
		access.WriteString(strconv.Quote(segment))
		access.WriteByte(']')
	}
	return access.String()
}

func renderOverlayValue(format OverlayValueFormat, assignment PortAssignment) (string, error) {
	switch format {
	case OverlayValueIntegerPort:
		return strconv.Itoa(assignment.Port), nil
	case OverlayValueHTTPURL:
		if !isLoopbackHost(assignment.Host) {
			return "", fmt.Errorf("HTTP URL host must be loopback")
		}
		return strconv.Quote("http://" + net.JoinHostPort(assignment.Host, strconv.Itoa(assignment.Port))), nil
	default:
		return "", fmt.Errorf("unsupported overlay value format %q", format)
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
