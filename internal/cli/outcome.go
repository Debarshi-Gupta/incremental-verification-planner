package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/config"
	"github.com/spf13/cobra"
)

func (c *commands) outcomeCommand() *cobra.Command {
	var changeKey, startedAt, acceptedAt, executionID string
	var acceptedCorrect bool
	var computeMillis, costMicrousd, correctionMinutes int64
	cmd := &cobra.Command{Use: "outcome", Short: "Record an accepted-change time and cost outcome", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(changeKey) == "" || strings.TrimSpace(startedAt) == "" || strings.TrimSpace(acceptedAt) == "" {
			return exitError{2, errors.New("--change-key, --started-at, and --accepted-at are required")}
		}
		if !cmd.Flags().Changed("accepted-correct") {
			return exitError{2, errors.New("--accepted-correct must explicitly be true or false")}
		}
		started, err := time.Parse(time.RFC3339, startedAt)
		if err != nil {
			return exitError{2, errors.New("--started-at must be an RFC3339 timestamp")}
		}
		accepted, err := time.Parse(time.RFC3339, acceptedAt)
		if err != nil || accepted.Before(started) {
			return exitError{2, errors.New("--accepted-at must be an RFC3339 timestamp at or after --started-at")}
		}
		return c.withConfig(cmd.Context(), func(cfg config.Config) error {
			client, err := newAPIClient(cfg)
			if err != nil {
				return err
			}
			repo, err := client.repository(cmd.Context(), cfg.Repository.Slug)
			if err != nil {
				return err
			}
			request := api.AcceptedChangeRequest{ChangeKey: strings.TrimSpace(changeKey), StartedAt: started.UTC(), AcceptedAt: accepted.UTC(), AcceptedCorrect: &acceptedCorrect,
				ComputeMillis: computeMillis, CostMicrousd: costMicrousd, HumanCorrectionMinutes: correctionMinutes, ExecutionID: strings.TrimSpace(executionID)}
			var result struct {
				ID              string `json:"id"`
				ChangeKey       string `json:"change_key"`
				AcceptedCorrect bool   `json:"accepted_correct"`
			}
			if err := client.request(cmd.Context(), http.MethodPost, "/api/v1/repositories/"+repo.ID.String()+"/accepted-changes", request, newIdempotencyKey(), &result); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Recorded accepted-change outcome %s for %s (accepted_correct=%t).\n", result.ID, result.ChangeKey, result.AcceptedCorrect)
			return nil
		})
	}}
	cmd.Flags().StringVar(&changeKey, "change-key", "", "stable pull request or change identifier")
	cmd.Flags().StringVar(&startedAt, "started-at", "", "RFC3339 timestamp when the change validation iteration began")
	cmd.Flags().StringVar(&acceptedAt, "accepted-at", "", "RFC3339 timestamp when the change was accepted")
	cmd.Flags().BoolVar(&acceptedCorrect, "accepted-correct", false, "mark the accepted change as correct after review")
	cmd.Flags().Int64Var(&computeMillis, "compute-ms", 0, "measured verification compute time in milliseconds")
	cmd.Flags().Int64Var(&costMicrousd, "cost-microusd", 0, "measured compute and CI cost in micro US dollars")
	cmd.Flags().Int64Var(&correctionMinutes, "human-correction-minutes", 0, "measured human review and correction time in minutes")
	cmd.Flags().StringVar(&executionID, "execution-id", "", "related IVP execution UUID")
	return cmd
}
