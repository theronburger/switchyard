package marketplace

import (
	"bytes"
	"testing"
)

func TestPlanLocalExcludeEditAppendsOwnedBlockWithoutReplacingExistingContent(t *testing.T) {
	current := []byte("# user-owned excludes\n.cache\nlast-line-without-newline")
	plan := PlanLocalExcludeEdit(current)
	if plan.Action != LocalExcludeAppend {
		t.Fatalf("action: got %q, want %q", plan.Action, LocalExcludeAppend)
	}
	if !bytes.HasPrefix(plan.ProposedContents, current) {
		t.Fatalf("proposed content did not preserve exact existing prefix: %q", plan.ProposedContents)
	}
	wantSuffix := append([]byte{'\n'}, localExcludeBlock...)
	if !bytes.HasSuffix(plan.ProposedContents, wantSuffix) {
		t.Fatalf("proposed content suffix: got %q, want suffix %q", plan.ProposedContents, wantSuffix)
	}
	if plan.ExpectedCurrentSHA256 != contentSHA256(current) {
		t.Fatalf("current hash: got %q", plan.ExpectedCurrentSHA256)
	}
}

func TestPlanLocalExcludeEditIsIdempotent(t *testing.T) {
	first := PlanLocalExcludeEdit(nil)
	if first.Action != LocalExcludeAppend {
		t.Fatalf("first action: got %q, want %q", first.Action, LocalExcludeAppend)
	}
	second := PlanLocalExcludeEdit(first.ProposedContents)
	if second.Action != LocalExcludeUnchanged {
		t.Fatalf("second action: got %q, want %q", second.Action, LocalExcludeUnchanged)
	}
	if !bytes.Equal(second.ProposedContents, first.ProposedContents) {
		t.Fatal("idempotent plan changed content")
	}
	want := []byte(
		"# >>> Switchyard managed local excludes >>>\n" +
			"/.switchyard.yaml\n" +
			"**/.switchyard.serverless.ts\n" +
			"# <<< Switchyard managed local excludes <<<\n",
	)
	if !bytes.Equal(first.ProposedContents, want) {
		t.Fatalf("empty-file plan: got %q, want %q", first.ProposedContents, want)
	}
}

func TestPlanLocalExcludeEditRefusesAmbiguousOwnedMarkers(t *testing.T) {
	tests := map[string][]byte{
		"missing end": []byte(localExcludeBeginMarker + "\n/.switchyard.yaml\n"),
		"missing begin": []byte(
			"/.switchyard.yaml\n" + localExcludeEndMarker + "\n",
		),
		"modified": []byte(
			localExcludeBeginMarker + "\n/.switchyard.yaml\n" + localExcludeEndMarker + "\n",
		),
		"duplicated": append(bytes.Clone(localExcludeBlock), localExcludeBlock...),
	}
	for name, current := range tests {
		t.Run(name, func(t *testing.T) {
			plan := PlanLocalExcludeEdit(current)
			if plan.Action != LocalExcludeRefuse {
				t.Fatalf("action: got %q, want %q", plan.Action, LocalExcludeRefuse)
			}
			if plan.ProposedContents != nil {
				t.Fatal("refusal unexpectedly proposed replacement content")
			}
		})
	}
}
