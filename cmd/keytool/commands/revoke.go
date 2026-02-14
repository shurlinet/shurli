package commands

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/urfave/cli"
)

// RevokeCommand removes a peer from authorized_keys
var RevokeCommand = cli.Command{
	Name:      "revoke",
	Usage:     "Remove a peer from authorized_keys",
	ArgsUsage: "<peer-id>",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "file, f",
			Value: "authorized_keys",
			Usage: "Path to authorized_keys file",
		},
	},
	Action: revokeAction,
}

func revokeAction(c *cli.Context) error {
	if c.NArg() != 1 {
		return fmt.Errorf("requires exactly one argument: <peer-id>")
	}

	peerIDStr := c.Args().First()
	authKeysPath := c.String("file")

	if err := auth.RemovePeer(authKeysPath, peerIDStr); err != nil {
		return err
	}

	color.Green("✓ Revoked peer: %s", peerIDStr[:min(16, len(peerIDStr))]+"...")
	fmt.Printf("  File: %s\n", authKeysPath)

	return nil
}
