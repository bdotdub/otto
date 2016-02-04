package plan

import (
	"fmt"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/otto/context"
)

// TaskExecutor is the interface that must be implemented to execute a
// task. The mapping of task "Type" to TaskExecutor is passed to Plan to
// execute.
type TaskExecutor interface {
	// Validate is called to validate the arguments and hint at return values.
	Validate(*ExecArgs) (*ExecResult, error)

	// Execute is called to perform the actual task.
	Execute(*ExecArgs) (*ExecResult, error)
}

// ExecArgs are the arguments given to a TaskExecutor.
type ExecArgs struct {
	// Ctx is the Otto context for this execution
	Ctx *context.Shared

	// Args is the map of arguments and their value. For validation,
	// the TaskArg value will be uninterpolated and thus shouldn't be
	// used. Keys can be used for validation.
	Args map[string]*TaskArg
}

// ExecResult is the result returned from a TaskExecutor
type ExecResult struct {
	// Values are the resulting named values from the execution.
	Values map[string]*TaskResult

	// Store can be used to put values into storage. This shouldn't be used
	// publicly. It is exposed in case it MUST be used but this is meant
	// to only be used by the "Store" task type. If *TaskResult is nil, then
	// it will be deleted from the store.
	Store map[string]*TaskResult
}

// Executor is the struct used to execute a plan.
type Executor struct {
	// Callback, if non-nil, will be called for various events during
	// execution. You can use this to get information and control the
	// execution.
	Callback func(ExecuteEvent)

	// TaskMap is the map of Task types to executors for that task
	TaskMap map[string]TaskExecutor
}

// Validate will validate the semantics of the plan. This checks that
// all variable access will resolve, all task types are valid, etc.
func (e *Executor) Validate(p *Plan, ctx *context.Shared) error {
	var err error

	// First verify the task types are valid and that the args
	// are well formed.
	for _, t := range p.Tasks {
		if _, ok := e.TaskMap[t.Type]; !ok {
			err = multierror.Append(err, fmt.Errorf("Unknown task type: %s", t.Type))
		}
	}

	// If we have errors at this point just return since the rest of the
	// checks will be difficult.
	if err != nil {
		return err
	}

	// Call "exec" in validation mode
	return e.exec(true, p, ctx)
}

// Execute is called to execute a plan.
//
// The configured Callback mechanism can be used to get regular progress
// events and control the execution. This function will block.
func (e *Executor) Execute(p *Plan, ctx *context.Shared) error {
	return e.exec(false, p, ctx)
}

func (e *Executor) exec(validate bool, p *Plan, ctx *context.Shared) error {
	var err error

	// These are the maps that store the variables and storage for execution
	varMap := make(map[string]*TaskResult)
	resultMap := make(map[string]*TaskResult)

	// Go through each task in serial
	for i, t := range p.Tasks {
		// Create the full map of available vars
		fullMap := make(map[string]*TaskResult)
		for k, v := range resultMap {
			fullMap[fmt.Sprintf("result.%s", k)] = v
		}
		for k, v := range varMap {
			fullMap[k] = v
		}

		// Validate the refs in the args to verify they match up properly.
		// We do this even if we're executing as an additional safety check
		for _, a := range t.Args {
			for _, ref := range a.Refs() {
				if _, ok := fullMap[ref]; !ok {
					err = multierror.Append(err, fmt.Errorf(
						"Task %d (%s): unknown reference: %s", i+1, t.Type, ref))
				}
			}
		}

		// Interpolate the args if we're not validating
		args := t.Args
		if !validate {
			args = make(map[string]*TaskArg)
			for k, raw := range t.Args {
				println(fmt.Sprintf("%#v", fullMap))
				arg, ierr := raw.Interpolate(fullMap)
				if ierr != nil {
					err = multierror.Append(err, fmt.Errorf(
						"Task %d (%s), arg %s: %s", i+1, t.Type, k, ierr))
					continue
				}

				args[k] = arg
			}
			if len(args) != len(t.Args) {
				// There was an error during interpolation
				break
			}
		}

		// Call Execute or Validate
		te := e.TaskMap[t.Type]
		var f func(*ExecArgs) (*ExecResult, error) = te.Execute
		if validate {
			f = te.Validate
		}
		result, verr := f(&ExecArgs{Ctx: ctx, Args: args})
		if verr != nil {
			err = multierror.Append(err, multierror.Prefix(
				verr, fmt.Sprintf("Task %d (%s): ", i+1, t.Type)))
			break
		}

		// Clear out the result map after every execute
		resultMap = make(map[string]*TaskResult)

		// If we have a result, build any new result values as well as
		// storage changes.
		if result != nil {
			// Keep track of the result types
			for k, v := range result.Values {
				resultMap[k] = v

				// In execution mode we can't have nil values
				if !validate && v == nil {
					delete(resultMap, k)
				}
			}

			// Keep track of storage
			for k, v := range result.Store {
				if v == nil {
					delete(varMap, k)
				} else {
					varMap[k] = v
				}
			}
		}
	}

	return err
}

// ExecuteEvent is an event that a callback can receive during execution.
// You must type switch on the various implementations below.
type ExecuteEvent interface{}
