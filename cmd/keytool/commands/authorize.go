package commands

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/urfave/cli"
)

// AuthorizeCommand adds a peer to authorized_keys
var AuthorizeCommand = cli.Command{
	Name:      "authorize",
	Usage:     "Add a peer to authorized_keys",
	ArgsUsage: "<peer-id>",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "file, f",
			Value: "authorized_keys",
			Usage: "Path to authorized_keys file",
		},
		cli.StringFlag{
			Name:  "comment, c",
			Usage: "Optional comment for this peer",
		},
	},
	Action: authorizeAction,
}

func authorizeAction(c *cli.Context) error {
	if c.NArg() != 1 {
		return fmt.Errorf("requires exactly one argument: <peer-id>")
	}

	peerIDStr := c.Args().First()
	authKeysPath := c.String("file")
	comment := c.String("comment")

	if err := auth.AddPeer(authKeysPath, peerIDStr, comment); err != nil {
		return err
	}

	color.Green("✓ Authorized peer: %s", peerIDStr[:min(16, len(peerIDStr))]+"...")
	if comment != "" {
		fmt.Printf("  Comment: %s\n", comment)
	}
	fmt.Printf("  File: %s\n", authKeysPath)

	return nil
}
