package main

import (
	"context"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/control/inventory"
)

func TestConfiguredRepositoryDiscoveryIsConcurrentOrderedAndIsolated(t *testing.T) {
	document := configuration.Document{Repositories: map[string]configuration.Repository{
		"second": {Enabled: true, DisplayName: "Second", Root: "/tmp/second"},
		"first":  {Enabled: true, DisplayName: "First", Root: "/tmp/first"},
		"off":    {Enabled: false, DisplayName: "Off", Root: "/tmp/off"},
	}}
	discovered := discoverConfiguredRepositories(context.Background(), time.Now(), document, func(
		_ context.Context, key string, profile configuration.Repository,
	) inventory.DiscoveryResult {
		if key == "second" {
			return inventory.DiscoveryResult{Errors: []inventory.DiscoveryError{{
				Code: inventory.ErrorRepositoryRemoteUnavailable, Message: "Remote unavailable.",
			}}}
		}
		return inventory.DiscoveryResult{Repository: &contractv1.Repository{
			ID: "repository_first", Adapter: key, RootPath: profile.Root,
			Worktrees: []contractv1.Worktree{},
		}}
	})
	if discovered.Complete || len(discovered.Repositories) != 1 ||
		discovered.Repositories[0].DisplayName != "First" || len(discovered.Alerts) != 1 {
		t.Fatalf("inventory: %+v", discovered)
	}
	if discovered.ProfileKeys["repository_first"] != "first" ||
		discovered.Profiles["repository_first"].Root != "/tmp/first" {
		t.Fatalf("profile maps: keys=%+v profiles=%+v", discovered.ProfileKeys, discovered.Profiles)
	}
}
