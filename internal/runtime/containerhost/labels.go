package containerhost

import (
	"errors"
	"regexp"
)

const (
	LabelManagedBy     = "com.theronburger.switchyard.managed-by"
	LabelEnvironmentID = "com.theronburger.switchyard.environment"
	LabelServiceID     = "com.theronburger.switchyard.service"
	LabelRunID         = "com.theronburger.switchyard.run"
	LabelInstanceID    = "com.theronburger.switchyard.instance"
	ManagedByValue     = "switchyard"
)

var identityValuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Identity struct {
	EnvironmentID string
	ServiceID     string
	RunID         string
	InstanceID    string
}

func (identity Identity) Validate() error {
	values := []struct {
		name  string
		value string
	}{
		{name: "environment", value: identity.EnvironmentID},
		{name: "service", value: identity.ServiceID},
		{name: "run", value: identity.RunID},
		{name: "instance", value: identity.InstanceID},
	}
	for _, candidate := range values {
		if !identityValuePattern.MatchString(candidate.value) {
			return errors.New(candidate.name + " identity is invalid")
		}
	}
	return nil
}

func (identity Identity) Labels() map[string]string {
	return map[string]string{
		LabelManagedBy:     ManagedByValue,
		LabelEnvironmentID: identity.EnvironmentID,
		LabelServiceID:     identity.ServiceID,
		LabelRunID:         identity.RunID,
		LabelInstanceID:    identity.InstanceID,
	}
}

type Ownership string

const (
	OwnershipOwned   Ownership = "owned"
	OwnershipForeign Ownership = "foreign"
	OwnershipPartial Ownership = "partial"
	OwnershipSpoofed Ownership = "spoofed"
)

func ClassifyLabels(labels map[string]string) (Ownership, Identity) {
	managedBy, hasManagedBy := labels[LabelManagedBy]
	environmentID, hasEnvironmentID := labels[LabelEnvironmentID]
	serviceID, hasServiceID := labels[LabelServiceID]
	runID, hasRunID := labels[LabelRunID]
	instanceID, hasInstanceID := labels[LabelInstanceID]

	hasIdentityLabel := hasEnvironmentID || hasServiceID || hasRunID || hasInstanceID
	if !hasManagedBy && !hasIdentityLabel {
		return OwnershipForeign, Identity{}
	}
	if !hasManagedBy || managedBy != ManagedByValue {
		return OwnershipSpoofed, Identity{}
	}
	if !hasEnvironmentID || !hasServiceID || !hasRunID || !hasInstanceID {
		return OwnershipPartial, Identity{}
	}
	identity := Identity{
		EnvironmentID: environmentID,
		ServiceID:     serviceID,
		RunID:         runID,
		InstanceID:    instanceID,
	}
	if identity.Validate() != nil {
		return OwnershipPartial, Identity{}
	}
	return OwnershipOwned, identity
}
