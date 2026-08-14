package marketplace

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

type ExistingServerlessProjection struct {
	Exists   bool
	Contents []byte
}

type ServerlessProjectionEditAction string

const (
	ServerlessProjectionCreate    ServerlessProjectionEditAction = "create"
	ServerlessProjectionReplace   ServerlessProjectionEditAction = "replace"
	ServerlessProjectionRemove    ServerlessProjectionEditAction = "remove"
	ServerlessProjectionUnchanged ServerlessProjectionEditAction = "unchanged"
	ServerlessProjectionRefuse    ServerlessProjectionEditAction = "refuse"
)

type ServerlessProjectionEditPlan struct {
	Action                ServerlessProjectionEditAction
	RelativePath          string
	ExpectedCurrentSHA256 string
	ProposedContents      []byte
	Reason                string
}

func PlanServerlessProjectionApply(
	current ExistingServerlessProjection,
	desired OwnedServerlessProjection,
) (ServerlessProjectionEditPlan, error) {
	if err := validateOwnedServerlessProjection(desired); err != nil {
		return ServerlessProjectionEditPlan{}, err
	}
	if !current.Exists {
		if len(current.Contents) != 0 {
			return ServerlessProjectionEditPlan{}, fmt.Errorf(
				"plan serverless projection apply: absent projection has content",
			)
		}
		return ServerlessProjectionEditPlan{
			Action:           ServerlessProjectionCreate,
			RelativePath:     desired.RelativePath,
			ProposedContents: bytes.Clone(desired.Contents),
		}, nil
	}

	currentHash := contentSHA256(current.Contents)
	if bytes.Equal(current.Contents, desired.Contents) {
		return ServerlessProjectionEditPlan{
			Action:                ServerlessProjectionUnchanged,
			RelativePath:          desired.RelativePath,
			ExpectedCurrentSHA256: currentHash,
			ProposedContents:      bytes.Clone(current.Contents),
		}, nil
	}
	if !isIntactOwnedServerlessProjection(current.Contents) {
		return ServerlessProjectionEditPlan{
			Action:                ServerlessProjectionRefuse,
			RelativePath:          desired.RelativePath,
			ExpectedCurrentSHA256: currentHash,
			Reason:                "existing projection is foreign or its owned payload was modified",
		}, nil
	}
	return ServerlessProjectionEditPlan{
		Action:                ServerlessProjectionReplace,
		RelativePath:          desired.RelativePath,
		ExpectedCurrentSHA256: currentHash,
		ProposedContents:      bytes.Clone(desired.Contents),
	}, nil
}

func PlanServerlessProjectionCleanup(
	current ExistingServerlessProjection,
	expected OwnedServerlessProjection,
) (ServerlessProjectionEditPlan, error) {
	if err := validateOwnedServerlessProjection(expected); err != nil {
		return ServerlessProjectionEditPlan{}, err
	}
	if !current.Exists {
		if len(current.Contents) != 0 {
			return ServerlessProjectionEditPlan{}, fmt.Errorf(
				"plan serverless projection cleanup: absent projection has content",
			)
		}
		return ServerlessProjectionEditPlan{
			Action:       ServerlessProjectionUnchanged,
			RelativePath: expected.RelativePath,
		}, nil
	}

	currentHash := contentSHA256(current.Contents)
	if bytes.Equal(current.Contents, expected.Contents) {
		return ServerlessProjectionEditPlan{
			Action:                ServerlessProjectionRemove,
			RelativePath:          expected.RelativePath,
			ExpectedCurrentSHA256: currentHash,
		}, nil
	}
	return ServerlessProjectionEditPlan{
		Action:                ServerlessProjectionRefuse,
		RelativePath:          expected.RelativePath,
		ExpectedCurrentSHA256: currentHash,
		Reason:                "projection no longer exactly matches the content Switchyard created",
	}, nil
}

func validateOwnedServerlessProjection(projection OwnedServerlessProjection) error {
	if projection.RelativePath == "" || path.IsAbs(projection.RelativePath) ||
		path.Clean(projection.RelativePath) != projection.RelativePath ||
		strings.HasPrefix(projection.RelativePath, "../") ||
		strings.ContainsRune(projection.RelativePath, '\\') {
		return fmt.Errorf("owned serverless projection has an invalid relative path")
	}
	payloadHash, intact := inspectOwnedServerlessProjection(projection.Contents)
	if !intact {
		return fmt.Errorf("owned serverless projection has an invalid ownership marker or payload hash")
	}
	if projection.PayloadSHA256 != payloadHash {
		return fmt.Errorf("owned serverless projection payload hash does not match its contents")
	}
	if projection.ContentSHA256 != contentSHA256(projection.Contents) {
		return fmt.Errorf("owned serverless projection content hash does not match its contents")
	}
	return nil
}

func isIntactOwnedServerlessProjection(contents []byte) bool {
	_, intact := inspectOwnedServerlessProjection(contents)
	return intact
}

func inspectOwnedServerlessProjection(contents []byte) (string, bool) {
	header, payload, found := bytes.Cut(contents, []byte("\n\n"))
	if !found {
		return "", false
	}
	headerLines := bytes.Split(header, []byte{'\n'})
	if len(headerLines) != 2 ||
		string(headerLines[0]) != serverlessProjectionOwnerHeaderPrefix+serverlessProjectionOwner {
		return "", false
	}
	hashLine := string(headerLines[1])
	if !strings.HasPrefix(hashLine, serverlessProjectionHashHeaderPrefix) {
		return "", false
	}
	payloadHash := strings.TrimPrefix(hashLine, serverlessProjectionHashHeaderPrefix)
	if len(payloadHash) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(payloadHash); err != nil {
		return "", false
	}
	if contentSHA256(payload) != payloadHash {
		return "", false
	}
	return payloadHash, true
}
