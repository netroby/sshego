// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package agent_test

import (
	"context"
	"log"
	"net"
	"os"

	ssh "github.com/glycerine/sshego/xendor/github.com/glycerine/xcryptossh"
	"github.com/glycerine/sshego/xendor/github.com/glycerine/xcryptossh/agent"
)

func ExampleClientAgent() {
	ctx, cancelctx := context.WithCancel(context.Background())
	defer cancelctx()

	halt := ssh.NewHalter()
	defer halt.ReqStop.Close()

	// ssh-agent has a UNIX socket under $SSH_AUTH_SOCK
	socket := os.Getenv("SSH_AUTH_SOCK")
	conn, err := net.Dial("unix", socket)
	if err != nil {
		log.Fatalf("net.Dial: %v", err)
	}
	agentClient := agent.NewClient(conn)
	config := &ssh.ClientConfig{
		User: "username",
		Auth: []ssh.AuthMethod{
			// Use a callback rather than PublicKeys
			// so we only consult the agent once the remote server
			// wants it.
			ssh.PublicKeysCallback(agentClient.Signers),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Config:          ssh.Config{Halt: halt},
	}

	sshc, err := ssh.Dial(ctx, "tcp", "localhost:22", config)
	if err != nil {
		log.Fatalf("Dial: %v", err)
	}
	// .. use sshc
	sshc.Close()
}
