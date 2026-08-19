package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const maximumMutationBodyBytes = 64 * 1024

type ActionError struct {
	Status   int
	Contract contractv1.ContractError
}

func (actionError *ActionError) Error() string {
	if actionError == nil || actionError.Contract.Message == "" {
		return "environment action failed"
	}
	return actionError.Contract.Message
}

func (handler *apiHandler) mutation(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path == "/v1/worktrees" {
		handler.createWorktree(response, request)
		return true
	}
	if worktreeID, matches := adoptWorktreePath(request.URL.Path); matches {
		handler.adoptWorktree(response, request, worktreeID)
		return true
	}
	if worktreeID, matches := archiveWorktreePath(request.URL.Path); matches {
		handler.archiveWorktree(response, request, worktreeID)
		return true
	}
	if request.URL.Path == "/v1/environments" {
		handler.startEnvironment(response, request)
		return true
	}
	environmentID, matches := stopEnvironmentPath(request.URL.Path)
	if !matches {
		return false
	}
	handler.stopEnvironment(response, request, environmentID)
	return true
}

func (handler *apiHandler) adoptWorktree(
	response http.ResponseWriter,
	request *http.Request,
	worktreeID string,
) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return
	}
	var mutation contractv1.AdoptWorktreeRequest
	if err := decodeMutationRequest(request, &mutation); err != nil || mutation.Validate() != nil ||
		mutation.WorktreeID != worktreeID {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "The worktree adoption request is invalid", false)
		return
	}
	if handler.config.WorkspaceActions == nil {
		writeError(response, http.StatusServiceUnavailable, "ACTIONS_UNAVAILABLE", "Workspace actions are unavailable", true)
		return
	}
	receipt, err := handler.config.WorkspaceActions.AdoptWorktree(request.Context(), mutation)
	handler.writeMutationResult(response, receipt, err)
}

func (handler *apiHandler) createWorktree(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return
	}
	var mutation contractv1.CreateWorktreeRequest
	if err := decodeMutationRequest(request, &mutation); err != nil || mutation.Validate() != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "The worktree creation request is invalid", false)
		return
	}
	if handler.config.WorkspaceActions == nil {
		writeError(response, http.StatusServiceUnavailable, "ACTIONS_UNAVAILABLE", "Workspace actions are unavailable", true)
		return
	}
	receipt, err := handler.config.WorkspaceActions.CreateWorktree(request.Context(), mutation)
	handler.writeMutationResult(response, receipt, err)
}

func (handler *apiHandler) archiveWorktree(
	response http.ResponseWriter,
	request *http.Request,
	worktreeID string,
) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return
	}
	var mutation contractv1.ArchiveWorktreeRequest
	if err := decodeMutationRequest(request, &mutation); err != nil || mutation.Validate() != nil ||
		mutation.WorktreeID != worktreeID {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "The worktree archive request is invalid", false)
		return
	}
	if handler.config.WorkspaceActions == nil {
		writeError(response, http.StatusServiceUnavailable, "ACTIONS_UNAVAILABLE", "Workspace actions are unavailable", true)
		return
	}
	receipt, err := handler.config.WorkspaceActions.ArchiveWorktree(request.Context(), mutation)
	handler.writeMutationResult(response, receipt, err)
}

func (handler *apiHandler) startEnvironment(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return
	}
	var mutation contractv1.StartEnvironmentRequest
	if err := decodeMutationRequest(request, &mutation); err != nil || mutation.Validate() != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "The environment start request is invalid", false)
		return
	}
	if handler.config.EnvironmentActions == nil {
		writeError(response, http.StatusServiceUnavailable, "ACTIONS_UNAVAILABLE", "Environment actions are unavailable", true)
		return
	}
	receipt, err := handler.config.EnvironmentActions.StartEnvironment(request.Context(), mutation)
	handler.writeMutationResult(response, receipt, err)
}

func (handler *apiHandler) stopEnvironment(
	response http.ResponseWriter,
	request *http.Request,
	environmentID string,
) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return
	}
	var mutation contractv1.StopEnvironmentRequest
	if err := decodeMutationRequest(request, &mutation); err != nil || mutation.Validate() != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "The environment stop request is invalid", false)
		return
	}
	if handler.config.EnvironmentActions == nil {
		writeError(response, http.StatusServiceUnavailable, "ACTIONS_UNAVAILABLE", "Environment actions are unavailable", true)
		return
	}
	receipt, err := handler.config.EnvironmentActions.StopEnvironment(request.Context(), environmentID, mutation)
	handler.writeMutationResult(response, receipt, err)
}

func (handler *apiHandler) writeMutationResult(
	response http.ResponseWriter,
	receipt contractv1.MutationReceipt,
	err error,
) {
	if err != nil {
		var actionError *ActionError
		if errors.As(err, &actionError) && validActionError(actionError) {
			writeContractError(response, actionError.Status, actionError.Contract)
			return
		}
		writeError(response, http.StatusInternalServerError, "ACTION_FAILED", "The environment action could not be accepted", true)
		return
	}
	if receipt.Validate() != nil {
		writeError(response, http.StatusInternalServerError, "INVALID_RECEIPT", "The environment action returned an invalid receipt", false)
		return
	}
	writeJSON(response, http.StatusAccepted, receipt)
}

func decodeMutationRequest(request *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("mutation content type must be application/json")
	}
	if request.ContentLength > maximumMutationBodyBytes {
		return errors.New("mutation body exceeds the safety limit")
	}
	reader := http.MaxBytesReader(nil, request.Body, maximumMutationBodyBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("mutation body must contain one JSON value")
	}
	return nil
}

func stopEnvironmentPath(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "environments" ||
		parts[3] != "stop" || !safePathIdentifier(parts[2]) {
		return "", false
	}
	return parts[2], true
}

func archiveWorktreePath(path string) (string, bool) {
	return worktreeActionPath(path, "archive")
}

func adoptWorktreePath(path string) (string, bool) {
	return worktreeActionPath(path, "adopt")
}

func worktreeActionPath(path string, action string) (string, bool) {
	const prefix = "/v1/worktrees/"
	suffix := "/" + action
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	worktreeID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if worktreeID == "" || strings.Contains(worktreeID, "/") || !safePathIdentifier(worktreeID) {
		return "", false
	}
	return worktreeID, true
}

func safePathIdentifier(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character == '/' || unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func validActionError(actionError *ActionError) bool {
	if actionError == nil || actionError.Contract.Code == "" || actionError.Contract.Message == "" {
		return false
	}
	switch actionError.Status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}
