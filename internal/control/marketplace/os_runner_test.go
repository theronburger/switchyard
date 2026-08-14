package marketplacecontrol

import (
	"context"
	"strings"
	"testing"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
)

func TestOSCommandRunnerUsesExactArgumentsAndWorkingDirectory(t *testing.T) {
	runner := OSCommandRunner{}
	output, err := runner.Run(context.Background(), marketplaceadapter.Invocation{
		Executable:       "/bin/pwd",
		WorkingDirectory: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output.Stdout)); got == "" {
		t.Fatal("working directory output is empty")
	}

	output, err = runner.Run(context.Background(), marketplaceadapter.Invocation{
		Executable: "/usr/bin/printf",
		Arguments:  []string{"%s|%s", "one two", "$(foreign)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(output.Stdout), "one two|$(foreign)"; got != want {
		t.Fatalf("exact argv output: got %q, want %q", got, want)
	}
}

func TestOSCommandRunnerRedactsFailuresAndHonorsCancellation(t *testing.T) {
	runner := OSCommandRunner{}
	secretArgument := "credential-looking-value"
	_, err := runner.Run(context.Background(), marketplaceadapter.Invocation{
		Executable: "/usr/bin/false",
		Arguments:  []string{secretArgument},
	})
	if err == nil || strings.Contains(err.Error(), secretArgument) {
		t.Fatalf("failure was absent or leaked arguments: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runner.Run(ctx, marketplaceadapter.Invocation{Executable: "/bin/pwd"})
	if err != context.Canceled {
		t.Fatalf("cancelled command: got %v", err)
	}
}
