package inventory

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

var opaqueIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func repositoryID(commonDirectory string, remote string) string {
	return stableOpaqueID("repo_", "repository-v1", commonDirectory, remote)
}

func worktreeID(repositoryID string, administrativeIdentity string) string {
	return stableOpaqueID("worktree_", "worktree-v1", repositoryID, administrativeIdentity)
}

func alertID(repositoryID string, worktreeID string, code AlertCode) string {
	return stableOpaqueID("alert_", "inventory-alert-v1", repositoryID, worktreeID, string(code))
}

func stableOpaqueID(prefix string, identityParts ...string) string {
	hasher := sha256.New()
	for _, part := range identityParts {
		hasher.Write([]byte{0})
		hasher.Write([]byte(part))
	}
	encoded := strings.ToLower(opaqueIDEncoding.EncodeToString(hasher.Sum(nil)))
	return prefix + encoded[:26]
}
