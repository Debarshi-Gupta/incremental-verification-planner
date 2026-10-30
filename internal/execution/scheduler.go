package execution

import (
	"context"
	"fmt"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/domain"
)

type RevalidationFunc func(context.Context) (PlanRevalidation, error)
type MaterializeFunc func(context.Context, domain.PlanUnit) (adapters.ExecutionCommand, error)
type ParseRequestFunc func(domain.PlanUnit) adapters.ParseRequest

type PlanExecution struct {
	Status  Status                  `json:"status"`
	Outcome domain.ExecutionOutcome `json:"outcome"`
	Results []Result                `json:"results"`
	Error   string                  `json:"error,omitempty"`
}

// RunPlan revalidates the plan immediately before every selected command and
// once after each command. A changed worktree prevents the next selective
// command and classifies the aggregate result as STALE.
func (r *Runner) RunPlan(ctx context.Context, plan domain.VerificationPlan, adapter adapters.Adapter, validate RevalidationFunc, materialize MaterializeFunc, parseRequest ParseRequestFunc) PlanExecution {
	result := PlanExecution{Status: StatusInfraError, Outcome: domain.OutcomeInfraFailure}
	if validate == nil || materialize == nil || adapter == nil {
		result.Error = "plan execution dependencies are incomplete"
		return result
	}
	if plan.Status != domain.PlanReady {
		result.Status = StatusEscalated
		result.Error = fmt.Sprintf("plan status %s requires full-suite handling", plan.Status)
		return result
	}
	units := append([]domain.PlanUnit(nil), plan.SelectedUnits...)
	units = append(units, plan.MandatoryUnits...)
	seen := map[string]bool{}
	deduplicated := units[:0]
	for _, unit := range units {
		key := unit.TargetRef
		if key == "" {
			key = unit.StableKey
		}
		if key == "" {
			result.Error = "plan contains a unit without a target reference"
			return result
		}
		if !seen[key] {
			seen[key] = true
			deduplicated = append(deduplicated, unit)
		}
	}
	units = deduplicated
	if len(units) == 0 {
		result.Error = "plan has no executable units"
		return result
	}
	anyFailure, anyInfra, fullVerification := false, false, plan.Mode == domain.ModeFull
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			result.Status = StatusCancelled
			result.Outcome = domain.OutcomeInfraFailure
			result.Error = err.Error()
			return result
		}
		state, err := validate(ctx)
		if err != nil {
			result.Status = StatusStalePlan
			result.Outcome = domain.OutcomeStale
			result.Error = err.Error()
			return result
		}
		if err = ValidatePlan(plan, state); err != nil {
			result.Status = StatusStalePlan
			result.Outcome = domain.OutcomeStale
			result.Error = err.Error()
			return result
		}
		command, err := materialize(ctx, unit)
		if err != nil {
			result.Status = StatusEscalated
			result.Outcome = domain.OutcomeInfraFailure
			result.Error = fmt.Sprintf("materialize %s: %v", unit.StableKey, err)
			return result
		}
		if plan.Mode == domain.ModeFull && !command.FullSuite {
			result.Status = StatusEscalated
			result.Outcome = domain.OutcomeInfraFailure
			result.Error = "full-verification plan materialized a selective command"
			return result
		}
		if !command.FullSuite {
			fullVerification = false
		}
		request := adapters.ParseRequest{}
		if parseRequest != nil {
			request = parseRequest(unit)
		}
		run := r.RunAndParse(ctx, adapter, command, request)
		result.Results = append(result.Results, run)
		if err := ctx.Err(); err != nil {
			result.Status = StatusCancelled
			result.Outcome = domain.OutcomeInfraFailure
			result.Error = err.Error()
			return result
		}
		if run.Status == StatusTimedOut || run.Status == StatusCancelled || run.Status == StatusInfraError || run.Status == StatusEscalated {
			anyInfra = true
		}
		if run.Outcome == domain.OutcomeTargetedFail || run.Outcome == domain.OutcomeFullVerificationFail {
			anyFailure = true
		}
		state, err = validate(ctx)
		if err != nil {
			result.Status = StatusStalePlan
			result.Outcome = domain.OutcomeStale
			result.Error = err.Error()
			return result
		}
		if err = ValidatePlan(plan, state); err != nil {
			result.Status = StatusStalePlan
			result.Outcome = domain.OutcomeStale
			result.Error = err.Error()
			return result
		}
		if anyInfra {
			result.Status = run.Status
			result.Outcome = domain.OutcomeInfraFailure
			result.Error = run.Error
			return result
		}
	}
	if fullVerification {
		if anyFailure {
			result.Status = StatusCompleted
			result.Outcome = domain.OutcomeFullVerificationFail
		} else {
			result.Status = StatusCompleted
			result.Outcome = domain.OutcomeFullVerificationPass
		}
	} else if anyFailure {
		result.Status = StatusCompleted
		result.Outcome = domain.OutcomeTargetedFail
	} else {
		result.Status = StatusCompleted
		result.Outcome = domain.OutcomeTargetedPass
	}
	return result
}

func (p PlanExecution) RequireFullFallback() bool {
	return p.Status == StatusStalePlan || p.Status == StatusEscalated || p.Status == StatusInfraError || p.Status == StatusTimedOut
}
