package marketplace

import "context"

type Invocation struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
}

type CommandOutput struct {
	Stdout []byte
}

type CommandRunner interface {
	Run(context.Context, Invocation) (CommandOutput, error)
}
