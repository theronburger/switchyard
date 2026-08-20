package containerhost

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

const PlanSchemaVersion = 1

var resourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type Planner struct {
	DockerBinary string
	Now          func() time.Time
}

func (planner Planner) Build(inventory Inventory, goals []Goal) (Plan, error) {
	canonicalInventory, err := NewInventory(inventory.Resources)
	if err != nil {
		return Plan{}, err
	}
	orderedGoals, err := validateGoals(goals)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now()
	if planner.Now != nil {
		now = planner.Now()
	}
	plan := Plan{
		SchemaVersion: PlanSchemaVersion,
		BaseRevision:  canonicalInventory.Revision,
		GeneratedAt:   now,
		Actions:       make([]Action, 0),
		Protections:   make([]Protection, 0),
	}

	resourcesByName := make(map[resourceNameKey][]Resource)
	resourcesByIdentity := make(map[resourceIdentityKey][]Resource)
	duplicateIdentities := make(map[resourceIdentityKey]struct{}, len(canonicalInventory.Duplicates))
	for _, resource := range canonicalInventory.Resources {
		nameKey := resourceNameKey{Kind: resource.Kind, Name: resource.Name}
		resourcesByName[nameKey] = append(resourcesByName[nameKey], resource)
		if resource.Ownership == OwnershipOwned {
			identityKey := resourceIdentityKey{Kind: resource.Kind, Identity: resource.Identity}
			resourcesByIdentity[identityKey] = append(resourcesByIdentity[identityKey], resource)
		}
	}
	for _, duplicate := range canonicalInventory.Duplicates {
		duplicateIdentities[resourceIdentityKey{Kind: duplicate.Kind, Identity: duplicate.Identity}] = struct{}{}
	}

	for _, goal := range orderedGoals {
		identityKey := resourceIdentityKey{Kind: goal.Kind, Identity: goal.Identity}
		if _, duplicate := duplicateIdentities[identityKey]; duplicate {
			plan.Protections = append(plan.Protections, Protection{
				Code: ProtectionDuplicateIdentity, ResourceKind: goal.Kind, ResourceName: goal.Name,
				Summary: "Multiple owned resources claim this identity; no action was planned.",
			})
			continue
		}

		existing := resourcesByIdentity[identityKey]
		if len(existing) == 1 {
			if existingProtection := protectionForImmutableMismatch(existing[0], goal); existingProtection != nil {
				plan.Protections = append(plan.Protections, *existingProtection)
				continue
			}
			plan.Actions = append(plan.Actions, planner.actionsForExisting(existing[0], goal)...)
			continue
		}
		if collision := resourcesByName[resourceNameKey{Kind: goal.Kind, Name: goal.Name}]; len(collision) > 0 {
			plan.Protections = append(plan.Protections, protectionForCollision(goal, collision))
			continue
		}
		if goal.DesiredState == DesiredRunning {
			plan.Actions = append(plan.Actions, planner.actionsForCreate(goal)...)
		}
	}
	return plan, nil
}

func (planner Planner) actionsForExisting(resource Resource, goal Goal) []Action {
	switch goal.DesiredState {
	case DesiredRunning:
		if resource.Kind == ResourceContainer && !resource.Running {
			return []Action{planner.newAction(ActionStart, resource, goal)}
		}
	case DesiredStopped:
		if resource.Kind == ResourceContainer && resource.Running {
			return []Action{planner.newAction(ActionStop, resource, goal)}
		}
	case DesiredAbsent:
		actions := make([]Action, 0, 2)
		if resource.Kind == ResourceContainer && resource.Running {
			actions = append(actions, planner.newAction(ActionStop, resource, goal))
		}
		actions = append(actions, planner.newAction(ActionRemove, resource, goal))
		return actions
	}
	return nil
}

func (planner Planner) actionsForCreate(goal Goal) []Action {
	resource := Resource{Kind: goal.Kind, Name: goal.Name, Identity: goal.Identity}
	create := planner.newAction(ActionCreate, resource, goal)
	if goal.Kind != ResourceContainer {
		return []Action{create}
	}
	start := planner.newAction(ActionStart, resource, goal)
	return []Action{create, start}
}

func (planner Planner) newAction(kind ActionKind, resource Resource, goal Goal) Action {
	image := goal.Image
	portBindings := goal.PortBindings
	if goal.Kind == ResourceContainer && goal.DesiredState != DesiredRunning {
		image = resource.Image
		portBindings = resource.PortBindings
	}
	action := Action{
		Kind:         kind,
		ResourceKind: goal.Kind,
		ResourceID:   resource.ID,
		ResourceName: resource.Name,
		Image:        image,
		PortBindings: clonePortBindings(portBindings),
		Environment:  append([]string(nil), goal.Environment...),
		Identity:     goal.Identity,
	}
	action.Command = expectedCommand(planner.executable(), action)
	return action
}

func (planner Planner) executable() string {
	if planner.DockerBinary == "" {
		return "docker"
	}
	return planner.DockerBinary
}

func validateGoals(goals []Goal) ([]Goal, error) {
	ordered := append([]Goal(nil), goals...)
	identities := make(map[resourceIdentityKey]struct{}, len(goals))
	names := make(map[resourceNameKey]struct{}, len(goals))
	for index := range ordered {
		goal := &ordered[index]
		if !goal.Kind.Valid() || !resourceNamePattern.MatchString(goal.Name) {
			return nil, errors.New("container resource goal is invalid")
		}
		if err := goal.Identity.Validate(); err != nil {
			return nil, err
		}
		if goal.DesiredState != DesiredRunning && goal.DesiredState != DesiredStopped &&
			goal.DesiredState != DesiredAbsent {
			return nil, errors.New("container resource desired state is invalid")
		}
		if goal.Kind == ResourceContainer && goal.DesiredState == DesiredRunning {
			if !safeImageReference(goal.Image) {
				return nil, errors.New("container image reference is invalid")
			}
			bindings, err := canonicalPortBindings(goal.PortBindings)
			if err != nil {
				return nil, err
			}
			goal.PortBindings = bindings
			if err := validateContainerEnvironment(goal.Environment); err != nil {
				return nil, err
			}
			sort.Strings(goal.Environment)
		} else if goal.Image != "" || len(goal.PortBindings) != 0 || len(goal.Environment) != 0 {
			return nil, errors.New("container image and ports are only valid for a running container goal")
		}
		identityKey := resourceIdentityKey{Kind: goal.Kind, Identity: goal.Identity}
		if _, duplicate := identities[identityKey]; duplicate {
			return nil, errors.New("duplicate container resource identity goal")
		}
		identities[identityKey] = struct{}{}
		nameKey := resourceNameKey{Kind: goal.Kind, Name: goal.Name}
		if _, duplicate := names[nameKey]; duplicate {
			return nil, errors.New("duplicate container resource name goal")
		}
		names[nameKey] = struct{}{}
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Kind != ordered[right].Kind {
			return ordered[left].Kind < ordered[right].Kind
		}
		return identitySortKey(ordered[left].Identity) < identitySortKey(ordered[right].Identity)
	})
	return ordered, nil
}

func validateContainerEnvironment(environment []string) error {
	seen := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" || len(entry) > 64*1024 || strings.ContainsRune(entry, 0) {
			return errors.New("container environment is invalid")
		}
		for index, character := range name {
			if !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9') {
				return errors.New("container environment is invalid")
			}
		}
		if _, duplicate := seen[name]; duplicate {
			return errors.New("container environment is duplicated")
		}
		seen[name] = struct{}{}
	}
	return nil
}

func protectionForImmutableMismatch(resource Resource, goal Goal) *Protection {
	if resource.Kind != ResourceContainer {
		return nil
	}
	configured, configuredError := canonicalPortBindings(resource.PortBindings)
	matchesObservedConfiguration := configuredError == nil && safeImageReference(resource.Image)
	if resource.Running {
		published, publishedError := canonicalPortBindings(resource.PublishedPortBindings)
		matchesObservedConfiguration = matchesObservedConfiguration && publishedError == nil &&
			portBindingsEqual(configured, published)
	}
	if goal.DesiredState == DesiredRunning {
		matchesObservedConfiguration = matchesObservedConfiguration && resource.Image == goal.Image &&
			portBindingsEqual(configured, goal.PortBindings)
	}
	if matchesObservedConfiguration {
		return nil
	}
	return &Protection{
		Code:         ProtectionImmutableMismatch,
		ResourceKind: resource.Kind,
		ResourceName: resource.Name,
		Summary:      "The owned container image or port bindings differ from the requested immutable configuration; it will not be changed.",
	}
}

func safeImageReference(image string) bool {
	return image != "" && len(image) <= 512 && !strings.Contains(image, "://") &&
		!strings.ContainsAny(image, " \t\r\n\x00")
}

func protectionForCollision(goal Goal, resources []Resource) Protection {
	code := ProtectionForeignCollision
	summary := "A foreign resource already uses this name; it will not be changed."
	for _, resource := range resources {
		if resource.Ownership == OwnershipPartial || resource.Ownership == OwnershipSpoofed {
			code = ProtectionUnsafeLabels
			summary = "A resource with incomplete or untrusted ownership labels uses this name; it will not be changed."
			break
		}
	}
	return Protection{Code: code, ResourceKind: goal.Kind, ResourceName: goal.Name, Summary: summary}
}

type resourceNameKey struct {
	Kind ResourceKind
	Name string
}

func expectedCommand(dockerBinary string, action Action) Command {
	arguments := make([]string, 0, 16)
	switch action.Kind {
	case ActionCreate:
		arguments = append(arguments, string(action.ResourceKind), "create")
		switch action.ResourceKind {
		case ResourceContainer:
			arguments = append(arguments, "--name", action.ResourceName)
			arguments = append(arguments, ownershipLabelArguments(action.Identity)...)
			for _, environment := range action.Environment {
				arguments = append(arguments, "--env", environment)
			}
			for _, binding := range action.PortBindings {
				arguments = append(arguments, "--publish", publishArgument(binding))
			}
			arguments = append(arguments, action.Image)
		case ResourceVolume:
			arguments = append(arguments, ownershipLabelArguments(action.Identity)...)
			arguments = append(arguments, action.ResourceName)
		case ResourceNetwork:
			arguments = append(arguments, ownershipLabelArguments(action.Identity)...)
			arguments = append(arguments, action.ResourceName)
		}
	case ActionStart:
		arguments = []string{"container", "start", "--", actionReference(action)}
	case ActionStop:
		arguments = []string{"container", "stop", "--time", "10", "--", actionReference(action)}
	case ActionRemove:
		arguments = []string{string(action.ResourceKind), "rm", "--", actionReference(action)}
	}
	return Command{Executable: dockerBinary, Arguments: arguments}
}

func ownershipLabelArguments(identity Identity) []string {
	labels := []struct {
		key   string
		value string
	}{
		{key: LabelManagedBy, value: ManagedByValue},
		{key: LabelEnvironmentID, value: identity.EnvironmentID},
		{key: LabelServiceID, value: identity.ServiceID},
		{key: LabelRunID, value: identity.RunID},
		{key: LabelInstanceID, value: identity.InstanceID},
	}
	arguments := make([]string, 0, len(labels)*2)
	for _, label := range labels {
		arguments = append(arguments, "--label", label.key+"="+label.value)
	}
	return arguments
}

func actionReference(action Action) string {
	if action.ResourceID != "" {
		return action.ResourceID
	}
	return action.ResourceName
}
