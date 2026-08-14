package mcp

import (
	"fmt"
	"sort"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const (
	maximumAttentionItems = 3
	maximumURLs           = 8
)

func BuildEnvironmentContext(
	snapshot contractv1.StatusSnapshot,
	environmentID string,
) (*contractv1.EnvironmentContext, error) {
	if environmentID == "" {
		return nil, nil
	}

	environment, found := environmentByID(snapshot.Environments, environmentID)
	if !found {
		return nil, fmt.Errorf("environment %q was not found", environmentID)
	}
	alerts := make(map[string]contractv1.Alert, len(snapshot.Alerts))
	for _, alert := range snapshot.Alerts {
		alerts[alert.ID] = alert
	}

	attention := make([]contractv1.AttentionItem, 0, min(len(environment.AttentionAlertIDs), maximumAttentionItems))
	for _, alertID := range environment.AttentionAlertIDs {
		alert, exists := alerts[alertID]
		if !exists {
			return nil, fmt.Errorf("environment %q references unknown alert", environmentID)
		}
		if len(attention) == maximumAttentionItems {
			continue
		}
		attention = append(attention, contractv1.AttentionItem{
			Severity: alert.Severity,
			Code:     alert.Code,
			Summary:  alert.Summary,
		})
	}

	urlNames := make([]string, 0, len(environment.URLs))
	for name := range environment.URLs {
		urlNames = append(urlNames, name)
	}
	sort.Strings(urlNames)
	urls := make(map[string]string, min(len(urlNames), maximumURLs))
	for _, name := range urlNames[:min(len(urlNames), maximumURLs)] {
		urls[name] = environment.URLs[name]
	}

	return &contractv1.EnvironmentContext{
		Revision:       environment.Revision,
		EnvironmentID:  environment.ID,
		DesiredState:   environment.DesiredState,
		ObservedState:  environment.ObservedState,
		Health:         environment.Health,
		URLs:           urls,
		AttentionCount: len(environment.AttentionAlertIDs),
		Attention:      attention,
		Truncated: len(environment.AttentionAlertIDs) > maximumAttentionItems ||
			len(environment.URLs) > maximumURLs,
	}, nil
}

func environmentByID(environments []contractv1.Environment, id string) (contractv1.Environment, bool) {
	for _, environment := range environments {
		if environment.ID == id {
			return environment, true
		}
	}
	return contractv1.Environment{}, false
}
