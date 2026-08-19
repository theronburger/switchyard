package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const maximumManifestBytes = 64 * 1024

type ManifestSource interface {
	ReadManifest(context.Context, string) (contents []byte, exists bool, err error)
}

type OSManifestSource struct{}

func (OSManifestSource) ReadManifest(
	ctx context.Context,
	manifestPath string,
) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	fileInfo, err := os.Lstat(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Size() > maximumManifestBytes {
		return nil, true, fmt.Errorf("local manifest is not a bounded regular file")
	}
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, true, err
	}
	if len(contents) > maximumManifestBytes {
		return nil, true, fmt.Errorf("local manifest exceeds size limit")
	}
	return contents, true, nil
}

type ExplicitRepositoryRoot struct {
	RootPath    string
	Adapter     string
	DisplayName string
}

type RepositoryConfiguration struct {
	RootPath                    string
	Adapter                     string
	DisplayName                 string
	Runtime                     RuntimeSettings
	Workspace                   WorkspaceSettings
	Source                      string
	ManifestPath                string
	RequiredLocalExcludePattern string
}

type ResolutionError struct {
	Code     string
	Message  string
	RootPath string
}

func (resolutionError ResolutionError) Error() string {
	return resolutionError.Message
}

type Resolution struct {
	Repositories []RepositoryConfiguration
	Errors       []ResolutionError
}

type Resolver struct {
	manifestSource ManifestSource
}

func NewResolver(manifestSource ManifestSource) (Resolver, error) {
	if manifestSource == nil {
		return Resolver{}, fmt.Errorf("manifest source is required")
	}
	return Resolver{manifestSource: manifestSource}, nil
}

func (resolver Resolver) Resolve(
	ctx context.Context,
	explicitRoots []ExplicitRepositoryRoot,
) Resolution {
	roots := append([]ExplicitRepositoryRoot(nil), explicitRoots...)
	sort.Slice(roots, func(left, right int) bool {
		return filepath.Clean(roots[left].RootPath) < filepath.Clean(roots[right].RootPath)
	})
	result := Resolution{}
	seenRoots := make(map[string]struct{}, len(roots))
	for _, explicitRoot := range roots {
		rootPath := filepath.Clean(explicitRoot.RootPath)
		if !filepath.IsAbs(rootPath) || rootPath == string(filepath.Separator) {
			result.Errors = append(result.Errors, resolutionError(
				"REPOSITORY_ROOT_INVALID",
				"An explicit repository root is invalid.",
				"",
			))
			continue
		}
		if _, duplicate := seenRoots[rootPath]; duplicate {
			result.Errors = append(result.Errors, resolutionError(
				"REPOSITORY_ROOT_DUPLICATED",
				"An explicit repository root was provided more than once.",
				rootPath,
			))
			continue
		}
		seenRoots[rootPath] = struct{}{}

		manifestPath := filepath.Join(rootPath, LocalManifestFilename)
		contents, exists, err := resolver.manifestSource.ReadManifest(ctx, manifestPath)
		if err != nil {
			result.Errors = append(result.Errors, resolutionError(
				"LOCAL_MANIFEST_UNREADABLE",
				"The repository-local Switchyard manifest could not be read.",
				rootPath,
			))
			continue
		}
		if exists {
			manifest, err := ParseLocalManifest(contents)
			if err != nil {
				result.Errors = append(result.Errors, resolutionError(
					"LOCAL_MANIFEST_INVALID",
					"The repository-local Switchyard manifest is invalid.",
					rootPath,
				))
				continue
			}
			displayName := manifest.Display.Name
			if displayName == "" {
				displayName = explicitRoot.DisplayName
			}
			if displayName == "" {
				displayName = filepath.Base(rootPath)
			}
			if !isDisplayName(displayName) {
				result.Errors = append(result.Errors, resolutionError(
					"EXPLICIT_REPOSITORY_CONFIGURATION_INVALID",
					"The explicit repository configuration is invalid.",
					rootPath,
				))
				continue
			}
			result.Repositories = append(result.Repositories, RepositoryConfiguration{
				RootPath:                    rootPath,
				Adapter:                     manifest.Adapter,
				DisplayName:                 displayName,
				Runtime:                     manifest.Runtime,
				Workspace:                   manifest.Workspace,
				Source:                      "local-manifest",
				ManifestPath:                manifestPath,
				RequiredLocalExcludePattern: LocalManifestExcludePattern,
			})
			continue
		}

		displayName := explicitRoot.DisplayName
		if displayName == "" {
			displayName = filepath.Base(rootPath)
		}
		if !isAdapterName(explicitRoot.Adapter) || !isDisplayName(displayName) {
			result.Errors = append(result.Errors, resolutionError(
				"EXPLICIT_REPOSITORY_CONFIGURATION_INVALID",
				"The explicit repository configuration is invalid.",
				rootPath,
			))
			continue
		}
		result.Repositories = append(result.Repositories, RepositoryConfiguration{
			RootPath:                    rootPath,
			Adapter:                     explicitRoot.Adapter,
			DisplayName:                 displayName,
			Source:                      "explicit",
			ManifestPath:                manifestPath,
			RequiredLocalExcludePattern: LocalManifestExcludePattern,
		})
	}
	return result
}

func resolutionError(code string, message string, rootPath string) ResolutionError {
	return ResolutionError{Code: code, Message: message, RootPath: rootPath}
}
