package tfmigrate

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/minamijoyo/tfmigrate/tfexec"
)

// MultiStateMigrator implements the Migrator interface.
type MultiStateMigrator struct {
	// fromTf is an instance of TerraformCLI which executes terraform command in a fromDir.
	fromTf tfexec.TerraformCLI

	// fromTf is an instance of TerraformCLI which executes terraform command in a toDir.
	toTf tfexec.TerraformCLI

	// actions is a list of multi state migration operations.
	actions []MultiStateAction
}

var _ Migrator = (*MultiStateMigrator)(nil)

// NewMultiStateMigrator returns a new MultiStateMigrator instance.
func NewMultiStateMigrator(fromDir string, toDir string, actions []MultiStateAction, o *MigratorOption) *MultiStateMigrator {
	fromTf := tfexec.NewTerraformCLI(tfexec.NewExecutor(fromDir, os.Environ()))
	toTf := tfexec.NewTerraformCLI(tfexec.NewExecutor(toDir, os.Environ()))
	if o != nil && len(o.ExecPath) > 0 {
		fromTf.SetExecPath(o.ExecPath)
		toTf.SetExecPath(o.ExecPath)
	}

	return &MultiStateMigrator{
		fromTf:  fromTf,
		toTf:    toTf,
		actions: actions,
	}
}

// plan computes new states by applying multi state migration operations to temporary states.
// It will fail if terraform plan detects any diffs with at least one new state.
// We intentional private this method not to expose internal states and unify
// the Migrator interface between a single and multi state migrator.
func (m *MultiStateMigrator) plan(ctx context.Context) (*tfexec.State, *tfexec.State, error) {
	// setup fromDir.
	fromCurrentState, fromSwitchBackToRemotekFunc, err := setupWorkDir(ctx, m.fromTf)
	if err != nil {
		return nil, nil, err
	}
	// switch back it to remote on exit.
	defer fromSwitchBackToRemotekFunc()

	// setup toDir.
	toCurrentState, toSwitchBackToRemotekFunc, err := setupWorkDir(ctx, m.toTf)
	if err != nil {
		return nil, nil, err
	}
	// switch back it to remote on exit.
	defer toSwitchBackToRemotekFunc()

	// computes new states by applying state migration operations to temporary states.
	log.Printf("[INFO] [migrator] compute new states (%s => %s)\n", m.fromTf.Dir(), m.toTf.Dir())
	var fromNewState, toNewState *tfexec.State
	for _, action := range m.actions {
		fromNewState, toNewState, err = action.MultiStateUpdate(ctx, m.fromTf, m.toTf, fromCurrentState, toCurrentState)
		if err != nil {
			return nil, nil, err
		}
		fromCurrentState = tfexec.NewState(fromNewState.Bytes())
		toCurrentState = tfexec.NewState(toNewState.Bytes())
	}

	// check if a plan in fromDir has no changes.
	log.Printf("[INFO] [migrator@%s] check diffs\n", m.fromTf.Dir())
	_, err = m.fromTf.Plan(ctx, fromCurrentState, "", "-input=false", "-no-color", "-detailed-exitcode")
	if err != nil {
		if exitErr, ok := err.(tfexec.ExitError); ok && exitErr.ExitCode() == 2 {
			log.Printf("[ERROR] [migrator@%s] unexpected diffs\n", m.fromTf.Dir())
			return nil, nil, fmt.Errorf("terraform plan command returns unexpected diffs: %s", err)
		}
		return nil, nil, err
	}

	// check if a plan in toDir has no changes.
	log.Printf("[INFO] [migrator@%s] check diffs\n", m.toTf.Dir())
	_, err = m.toTf.Plan(ctx, toCurrentState, "", "-input=false", "-no-color", "-detailed-exitcode")
	if err != nil {
		if exitErr, ok := err.(tfexec.ExitError); ok && exitErr.ExitCode() == 2 {
			log.Printf("[ERROR] [migrator@%s] unexpected diffs\n", m.toTf.Dir())
			return nil, nil, fmt.Errorf("terraform plan command returns unexpected diffs: %s", err)
		}
		return nil, nil, err
	}

	return fromCurrentState, toCurrentState, nil
}

// Plan computes new states by applying multi state migration operations to temporary states.
// It will fail if terraform plan detects any diffs with at least one new state.
func (m *MultiStateMigrator) Plan(ctx context.Context) error {
	log.Printf("[INFO] [migrator] multi start state migrator plan\n")
	_, _, err := m.plan(ctx)
	if err != nil {
		return err
	}
	log.Printf("[INFO] [migrator] multi state migrator plan success!\n")
	return nil
}

// Apply computes new states and pushes them to remote states.
// It will fail if terraform plan detects any diffs with at least one new state.
// We are intended to this is used for state refactoring.
// Any state migration operations should not break any real resources.
func (m *MultiStateMigrator) Apply(ctx context.Context) error {
	// Check if new states don't have any diffs compared to real resources
	// before push new states to remote.
	log.Printf("[INFO] [migrator] start multi state migrator plan phase for apply\n")
	fromState, toState, err := m.plan(ctx)
	if err != nil {
		return err
	}

	// push the new states to remote.
	// We push toState before fromState, because when moving resources across
	// states, write them to new state first and then remove them from old one.
	log.Printf("[INFO] [migrator] start multi state migrator apply phase\n")
	log.Printf("[INFO] [migrator@%s] push the new state to remote\n", m.toTf.Dir())
	err = m.toTf.StatePush(ctx, toState)
	if err != nil {
		return err
	}
	log.Printf("[INFO] [migrator@%s] push the new state to remote\n", m.fromTf.Dir())
	err = m.fromTf.StatePush(ctx, fromState)
	if err != nil {
		return err
	}
	log.Printf("[INFO] [migrator] multi state migrator apply success!\n")
	return nil
}