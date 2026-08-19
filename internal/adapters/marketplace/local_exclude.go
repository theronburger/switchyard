package marketplace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

const (
	localExcludeBeginMarker = "# >>> Switchyard managed local excludes >>>"
	localExcludeEndMarker   = "# <<< Switchyard managed local excludes <<<"
)

var legacyLocalExcludeBlock = []byte(
	localExcludeBeginMarker + "\n" +
		"/.switchyard.yaml\n" +
		"**/.switchyard.serverless.ts\n" +
		localExcludeEndMarker + "\n",
)

var interimLocalExcludeBlock = []byte(
	localExcludeBeginMarker + "\n" +
		"/.switchyard.yaml\n" +
		"**/.switchyard.serverless.ts\n" +
		"**/.switchyard.dynamodb.cjs\n" +
		localExcludeEndMarker + "\n",
)

var endpointLocalExcludeBlock = []byte(
	localExcludeBeginMarker + "\n" +
		"/.switchyard.yaml\n" +
		"**/.switchyard.serverless.ts\n" +
		"**/.switchyard.endpoints.cjs\n" +
		localExcludeEndMarker + "\n",
)

var localExcludeBlock = []byte(
	localExcludeBeginMarker + "\n" +
		"/.switchyard.yaml\n" +
		"/.switchyard.env.cjs\n" +
		"**/.switchyard.serverless.ts\n" +
		"**/.switchyard.endpoints.cjs\n" +
		localExcludeEndMarker + "\n",
)

type LocalExcludeEditAction string

const (
	LocalExcludeAppend    LocalExcludeEditAction = "append"
	LocalExcludeUnchanged LocalExcludeEditAction = "unchanged"
	LocalExcludeRefuse    LocalExcludeEditAction = "refuse"
)

type LocalExcludeEditPlan struct {
	Action                LocalExcludeEditAction
	ExpectedCurrentSHA256 string
	ProposedContents      []byte
	Reason                string
}

func PlanLocalExcludeEdit(currentContents []byte) LocalExcludeEditPlan {
	currentHash := contentSHA256(currentContents)
	beginCount := bytes.Count(currentContents, []byte(localExcludeBeginMarker))
	endCount := bytes.Count(currentContents, []byte(localExcludeEndMarker))
	blockCount := bytes.Count(currentContents, localExcludeBlock)

	if beginCount == 1 && endCount == 1 && blockCount == 1 {
		return LocalExcludeEditPlan{
			Action:                LocalExcludeUnchanged,
			ExpectedCurrentSHA256: currentHash,
			ProposedContents:      bytes.Clone(currentContents),
		}
	}
	if beginCount == 1 && endCount == 1 && bytes.Count(currentContents, legacyLocalExcludeBlock) == 1 {
		return LocalExcludeEditPlan{
			Action:                LocalExcludeAppend,
			ExpectedCurrentSHA256: currentHash,
			ProposedContents:      bytes.Replace(bytes.Clone(currentContents), legacyLocalExcludeBlock, localExcludeBlock, 1),
		}
	}
	if beginCount == 1 && endCount == 1 && bytes.Count(currentContents, interimLocalExcludeBlock) == 1 {
		return LocalExcludeEditPlan{
			Action:                LocalExcludeAppend,
			ExpectedCurrentSHA256: currentHash,
			ProposedContents:      bytes.Replace(bytes.Clone(currentContents), interimLocalExcludeBlock, localExcludeBlock, 1),
		}
	}
	if beginCount == 1 && endCount == 1 && bytes.Count(currentContents, endpointLocalExcludeBlock) == 1 {
		return LocalExcludeEditPlan{
			Action:                LocalExcludeAppend,
			ExpectedCurrentSHA256: currentHash,
			ProposedContents:      bytes.Replace(bytes.Clone(currentContents), endpointLocalExcludeBlock, localExcludeBlock, 1),
		}
	}
	if beginCount != 0 || endCount != 0 {
		return LocalExcludeEditPlan{
			Action:                LocalExcludeRefuse,
			ExpectedCurrentSHA256: currentHash,
			Reason:                "existing Switchyard marker block is incomplete, duplicated, or modified",
		}
	}

	proposedContents := bytes.Clone(currentContents)
	if len(proposedContents) > 0 && proposedContents[len(proposedContents)-1] != '\n' {
		proposedContents = append(proposedContents, '\n')
	}
	proposedContents = append(proposedContents, localExcludeBlock...)
	return LocalExcludeEditPlan{
		Action:                LocalExcludeAppend,
		ExpectedCurrentSHA256: currentHash,
		ProposedContents:      proposedContents,
	}
}

func contentSHA256(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
