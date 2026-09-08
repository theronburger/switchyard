package localapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/theronburger/switchyard/internal/workspaces"
)

type Server struct {
	Engine *workspaces.Engine
	Token  string
}

func (s Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Token)) != 1 {
		writeError(w, http.StatusUnauthorized, "Cannot connect. Reopen Switchyard to reconnect.")
		return
	}
	if r.Header.Get("X-Switchyard-Version") != "3" {
		writeError(w, 426, "App and helper versions differ. Reopen Switchyard to update its helper.")
		return
	}
	var result any
	var err error
	switch r.Method + " " + r.URL.Path {
	case "GET /api/status":
		result, err = s.Engine.Status(r.Context())
	case "GET /api/context":
		var status workspaces.Snapshot
		status, err = s.Engine.Status(r.Context())
		if err == nil {
			result, err = workspaceAt(r.Context(), status, r.URL.Query().Get("path"))
		}
	case "GET /api/config":
		result = s.Engine.Config()
	case "POST /api/config":
		var config workspaces.Config
		err = decode(r, &config)
		if err == nil {
			err = s.Engine.SetConfig(config)
		}
		result = map[string]bool{"ok": err == nil}
	case "POST /api/choices":
		var request struct {
			Path     string   `json:"path"`
			Target   string   `json:"target"`
			Services []string `json:"services"`
		}
		err = decode(r, &request)
		if err == nil {
			err = s.Engine.SetChoices(request.Path, workspaces.Choices{Target: request.Target, Services: request.Services})
		}
		result = map[string]bool{"ok": err == nil}
	case "POST /api/run", "POST /api/prepare":
		var request workspaces.RunRequest
		err = decode(r, &request)
		if err == nil {
			request.PrepareOnly = r.URL.Path == "/api/prepare"
			result, err = s.Engine.Run(r.Context(), request)
		}
	case "POST /api/stop":
		var request struct {
			Path string `json:"path"`
		}
		err = decode(r, &request)
		if err == nil {
			result, err = s.Engine.Stop(r.Context(), request.Path)
		}
	case "GET /api/logs":
		result, err = s.Engine.Logs(r.URL.Query().Get("path"), 32*1024)
	case "GET /api/prune-plan":
		result, err = s.Engine.PrunePlan(r.Context(), r.URL.Query().Get("path"))
	case "POST /api/prune":
		var request struct {
			Path string `json:"path"`
		}
		err = decode(r, &request)
		if err == nil {
			err = s.Engine.Prune(r.Context(), request.Path)
		}
		result = map[string]bool{"ok": err == nil}
	case "POST /api/create":
		var request workspaces.CreateRequest
		err = decode(r, &request)
		var path string
		if err == nil {
			path, err = s.Engine.Create(r.Context(), request)
		}
		result = map[string]string{"path": path}
	default:
		writeError(w, 404, "No such action.")
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func decode(r *http.Request, value any) error {
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("The request is incomplete or invalid.")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("The request must contain one JSON object.")
	}
	return nil
}
func writeError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func workspaceAt(ctx context.Context, status workspaces.Snapshot, path string) (workspaces.Workspace, error) {
	physical, err := workspaces.ResolvePath(ctx, path)
	if err != nil {
		return workspaces.Workspace{}, err
	}
	for _, workspace := range status.Workspaces {
		if workspace.Path == physical {
			return workspace, nil
		}
	}
	return workspaces.Workspace{}, errors.New("This directory is not a worktree of a configured repository. Add its repository in Switchyard.")
}
