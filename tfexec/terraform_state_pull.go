package tfexec

import "context"

// StatePull returns the current tfstate from remote.
func (c *terraformCLI) StatePull(ctx context.Context) (State, error) {
	stdout, _, err := c.Run(ctx, "state", "pull")
	if err != nil {
		return "", err
	}

	return State(stdout), nil
}