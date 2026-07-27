package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/chenhg5/cc-connect/mcp/askuser"
)

func runAskUserMCPStdio(args []string) {
	fs := flag.NewFlagSet("askuser-mcp-stdio", flag.ExitOnError)
	socketPath := fs.String("socket", "", "path to cc-connect ask-user MCP Unix socket")
	sessionKey := fs.String("session-key", "", "cc-connect ask-user session key")
	_ = fs.Parse(args)

	socket := *socketPath
	if socket == "" {
		socket = os.Getenv(askuser.EnvAskUserSocketPath)
	}
	key := *sessionKey
	if key == "" {
		key = os.Getenv(askuser.EnvSessionKey)
	}

	if socket == "" || key == "" {
		fmt.Fprintf(os.Stderr, "usage: cc-connect askuser-mcp-stdio --socket PATH --session-key KEY (or env %s/%s)\n", askuser.EnvAskUserSocketPath, askuser.EnvSessionKey)
		os.Exit(2)
	}
	if err := askuser.ServeStdio(context.Background(), os.Stdin, os.Stdout, socket, key); err != nil {
		fmt.Fprintf(os.Stderr, "askuser-mcp-stdio: %v\n", err)
		os.Exit(1)
	}
}
