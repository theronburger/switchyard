package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
			"schemaVersion": 1,
			"version":       version,
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Fprintln(os.Stderr, "usage: switchyard version")
	os.Exit(2)
}
