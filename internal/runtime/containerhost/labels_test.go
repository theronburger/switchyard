package containerhost

import "testing"

func TestClassifyLabelsRequiresTheAtomicOwnershipSet(t *testing.T) {
	identity := testIdentity("one")
	tests := []struct {
		name   string
		labels map[string]string
		want   Ownership
	}{
		{name: "owned", labels: identity.Labels(), want: OwnershipOwned},
		{name: "foreign", labels: map[string]string{"team": "local"}, want: OwnershipForeign},
		{name: "partial", labels: map[string]string{
			LabelManagedBy: ManagedByValue, LabelEnvironmentID: identity.EnvironmentID,
		}, want: OwnershipPartial},
		{name: "spoofed marker", labels: map[string]string{
			LabelManagedBy: "not-switchyard", LabelEnvironmentID: identity.EnvironmentID,
			LabelServiceID: identity.ServiceID, LabelRunID: identity.RunID, LabelInstanceID: identity.InstanceID,
		}, want: OwnershipSpoofed},
		{name: "identity labels without marker", labels: map[string]string{
			LabelEnvironmentID: identity.EnvironmentID, LabelServiceID: identity.ServiceID,
			LabelRunID: identity.RunID, LabelInstanceID: identity.InstanceID,
		}, want: OwnershipSpoofed},
		{name: "invalid owned value", labels: map[string]string{
			LabelManagedBy: ManagedByValue, LabelEnvironmentID: "../unsafe",
			LabelServiceID: identity.ServiceID, LabelRunID: identity.RunID, LabelInstanceID: identity.InstanceID,
		}, want: OwnershipPartial},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, gotIdentity := ClassifyLabels(test.labels)
			if got != test.want {
				t.Fatalf("ownership: got %q, want %q", got, test.want)
			}
			if got == OwnershipOwned && gotIdentity != identity {
				t.Fatalf("identity: got %+v, want %+v", gotIdentity, identity)
			}
			if got != OwnershipOwned && gotIdentity != (Identity{}) {
				t.Fatalf("unowned labels exposed an identity: %+v", gotIdentity)
			}
		})
	}
}
