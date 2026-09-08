package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/localapi"
	"github.com/theronburger/switchyard/internal/workspaces"
)

type arguments struct {
	command, root, target, confirmed, requestID, base string
	positional                                        []string
	wait, json, all                                   bool
}

func parseArgs(args []string) (arguments, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return arguments{}, err
	}
	folder := "Switchyard Rebuild"
	if buildChannel == "release" {
		folder = "Switchyard"
	}
	o := arguments{command: "help", root: filepath.Join(home, "Library", "Application Support", folder)}
	if len(args) > 0 {
		o.command = args[0]
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		value := args[i]
		switch value {
		case "--root", "--target", "--confirm-target", "--request-id", "--base":
			if i+1 == len(args) {
				return o, fmt.Errorf("%s requires a value", value)
			}
			i++
			switch value {
			case "--root":
				o.root = args[i]
			case "--target":
				o.target = args[i]
			case "--confirm-target":
				o.confirmed = args[i]
			case "--request-id":
				o.requestID = args[i]
			case "--base":
				o.base = args[i]
			}
		case "--wait":
			o.wait = true
		case "--json":
			o.json = true
		case "--all":
			o.all = true
		case "--stdio":
		default:
			if strings.HasPrefix(value, "--") {
				return o, fmt.Errorf("Unknown option %s", value)
			}
			o.positional = append(o.positional, value)
		}
	}
	if !filepath.IsAbs(o.root) {
		return o, errors.New("--root must be an absolute path")
	}
	return o, nil
}
func runCLI(o arguments) error {
	ctx := context.Background()
	client := localapi.Client{Root: o.root}
	var result any
	switch o.command {
	case "status":
		if o.all {
			var snapshot workspaces.Snapshot
			err := client.Request(ctx, "GET", "/api/status", nil, &snapshot)
			if err != nil {
				return err
			}
			result = snapshot
		} else {
			path := "."
			if len(o.positional) > 0 {
				path = o.positional[0]
			}
			workspace, err := resolveWorkspace(ctx, client, path)
			if err != nil {
				return err
			}
			result = workspace
		}
	case "doctor":
		var snapshot workspaces.Snapshot
		if err := client.Request(ctx, "GET", "/api/status", nil, &snapshot); err != nil {
			return err
		}
		result = map[string]any{"connected": true, "version": snapshot.Version, "workspaces": len(snapshot.Workspaces)}
	case "config":
		if len(o.positional) == 2 && o.positional[0] == "set" {
			data, err := os.ReadFile(o.positional[1])
			if err != nil {
				return err
			}
			var config workspaces.Config
			if err = json.Unmarshal(data, &config); err != nil {
				return errors.New("The configuration file is not valid JSON.")
			}
			if err = client.Request(ctx, "POST", "/api/config", config, nil); err != nil {
				return err
			}
			result = map[string]bool{"saved": true}
		} else {
			var config workspaces.Config
			if err := client.Request(ctx, "GET", "/api/config", nil, &config); err != nil {
				return err
			}
			result = config
		}
	case "run", "start", "prepare":
		path := "."
		if len(o.positional) > 0 {
			path = o.positional[0]
		}
		workspace, err := resolveWorkspace(ctx, client, path)
		if err != nil {
			return err
		}
		selected := workspace.Choices.Services
		if len(o.positional) > 1 {
			selected = o.positional[1:]
		}
		target := o.target
		if target == "" {
			target = workspace.Choices.Target
		}
		id := o.requestID
		if id == "" {
			id = randomID()
		}
		endpoint := "/api/run"
		if o.command == "prepare" {
			endpoint = "/api/prepare"
		}
		request := workspaces.RunRequest{Path: workspace.Path, RequestID: id, Target: target, Services: selected, ConfirmedTarget: o.confirmed}
		var run workspaces.Run
		if err = client.Request(ctx, "POST", endpoint, request, &run); err != nil {
			return err
		}
		if o.wait {
			run, err = waitForRun(ctx, client, workspace.Path, run.ID, o.command == "prepare")
			if err != nil {
				return err
			}
		}
		if o.command == "prepare" && o.wait && run.Step != "Prepared" {
			return errors.New("Preparation was cancelled.")
		}
		result = run
	case "stop", "logs", "prune-plan", "prune", "open":
		path := "."
		if len(o.positional) > 0 {
			path = o.positional[0]
		}
		workspace, err := resolveWorkspace(ctx, client, path)
		if err != nil {
			return err
		}
		switch o.command {
		case "stop":
			var run workspaces.Run
			if err = client.Request(ctx, "POST", "/api/stop", map[string]string{"path": workspace.Path}, &run); err != nil {
				return err
			}
			if o.wait {
				run, err = waitForRun(ctx, client, workspace.Path, run.ID, true)
				if err != nil {
					return err
				}
			}
			result = run
		case "logs":
			var logs workspaces.LogOutput
			if err = client.Request(ctx, "GET", "/api/logs?path="+url.QueryEscape(workspace.Path), nil, &logs); err != nil {
				return err
			}
			if !o.json {
				fmt.Print(logs.Text)
				return nil
			}
			result = logs
		case "prune-plan":
			var plan workspaces.PrunePlan
			if err = client.Request(ctx, "GET", "/api/prune-plan?path="+url.QueryEscape(workspace.Path), nil, &plan); err != nil {
				return err
			}
			result = plan
		case "prune":
			if err = client.Request(ctx, "POST", "/api/prune", map[string]string{"path": workspace.Path}, nil); err != nil {
				return err
			}
			result = map[string]bool{"removed": true}
		case "open":
			scheme := "switchyard-rebuild"
			if buildChannel == "release" {
				scheme = "switchyard"
			}
			return exec.Command("/usr/bin/open", scheme+"://workspace?path="+url.QueryEscape(workspace.Path)).Run()
		}
	case "create":
		if len(o.positional) != 2 {
			return errors.New("Usage: sy create REPOSITORY_ID BRANCH [--base REF]")
		}
		var created map[string]string
		if err := client.Request(ctx, "POST", "/api/create", workspaces.CreateRequest{RepositoryID: o.positional[0], Branch: o.positional[1], Base: o.base}, &created); err != nil {
			return err
		}
		result = created
	default:
		fmt.Println("Switchyard — workspaces and local app instances\n\nsy status [PATH] [--all]\nsy run [PATH] [SERVICE…] [--target TARGET] [--wait]\nsy prepare [PATH] --wait\nsy stop [PATH] [--wait]\nsy logs [PATH]\nsy prune-plan PATH\nsy prune PATH\nsy create REPOSITORY_ID BRANCH [--base REF]\nsy open [PATH]\nsy config [set FILE]\nsy doctor\n\nAdd --json for machine-readable output. Configuration stays outside repositories.")
		return nil
	}
	encoder := json.NewEncoder(os.Stdout)
	if !o.json {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(result)
}
func resolveWorkspace(ctx context.Context, client localapi.Client, path string) (workspaces.Workspace, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return workspaces.Workspace{}, err
	}
	var workspace workspaces.Workspace
	err = client.Request(ctx, "GET", "/api/context?path="+url.QueryEscape(absolute), nil, &workspace)
	return workspace, err
}
func waitForRun(ctx context.Context, client localapi.Client, path, id string, untilStopped bool) (workspaces.Run, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	for {
		var workspace workspaces.Workspace
		err := client.Request(ctx, "GET", "/api/context?path="+url.QueryEscape(path), nil, &workspace)
		if err != nil {
			return workspaces.Run{}, err
		}
		if workspace.Run == nil || workspace.Run.ID != id {
			return workspaces.Run{}, errors.New("This run ended or was replaced. Check the current workspace status.")
		}
		run := *workspace.Run
		switch run.State {
		case "failed":
			return run, errors.New(run.Error)
		case "running":
			if !untilStopped {
				return run, nil
			}
		case "stopped":
			if untilStopped {
				return run, nil
			}
			return run, errors.New("The start was cancelled.")
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
