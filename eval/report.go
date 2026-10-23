package eval

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RenderMarkdown produces a self-contained evaluation report. It intentionally
// distinguishes measured values from unavailable ones instead of filling gaps.
func RenderMarkdown(report *Report) string {
	if report == nil {
		return "# PARADOX Evaluation Report\n\nNo report data.\n"
	}
	var b strings.Builder
	b.WriteString("# PARADOX Evaluation Report\n\n")
	fmt.Fprintf(&b, "- **Eval ID:** `%s`\n", report.EvalID)
	fmt.Fprintf(&b, "- **Suite:** `%s`\n", report.Suite)
	fmt.Fprintf(&b, "- **Created:** %s\n", report.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Provider / model:** `%s` / `%s`\n", report.Provider, report.Model)
	if report.GitCommit != "" {
		fmt.Fprintf(&b, "- **Git commit:** `%s`\n", report.GitCommit)
	}
	fmt.Fprintf(&b, "- **Trials per mode:** %d\n", report.Trials)
	fmt.Fprintf(&b, "- **Modes:** %s\n", modeNames(report.Modes))
	b.WriteString("\n")

	b.WriteString("## Method\n\n")
	b.WriteString("Every mode runs the same task prompts, model, budget, retry policy, and tool timeout. ")
	b.WriteString("`baseline` executes tools and records claims without dependency tracking or active verification. ")
	b.WriteString("`evidence` tracks evidence dependencies and consistency but does not generate falsification tests. ")
	b.WriteString("`paradox` additionally generates executable verification plans, detects contradictions, retracts invalidated premises and claims, and asks the agent to replan.\n\n")
	b.WriteString("The internal suite is deterministic and exercises the harness. It is not evidence about the quality of a remote model. ")
	b.WriteString("No result below is a real-model benchmark unless the provider and model fields name a real provider and model.\n\n")

	b.WriteString("## Summary\n\n")
	b.WriteString("| Mode | Runs | Success | Mean score | Unsupported claim rate | Incorrect retraction rate | Verification accuracy | Reproducibility | Tokens | Cost | Mean time |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		aggregate, ok := report.ByMode[string(mode)]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %s | %.3f | %s | %s | %s | %s | %d | %s | %s |\n",
			mode, aggregate.Runs, FormatPercent(aggregate.SuccessRate), aggregate.MeanScore,
			ratio(aggregate.UnsupportedClaims, aggregate.ActiveClaims),
			ratio(aggregate.IncorrectRetractions, aggregate.RetractionsEvaluated),
			ratio(aggregate.VerificationCorrect, aggregate.VerificationCorrect+aggregate.VerificationIncorrect),
			reproducibility(aggregate), aggregate.TotalTokens, cost(aggregate), duration(aggregate.MeanDurationMS))
	}
	fmt.Fprintf(&b, "| **all** | %d | %s | %.3f | %s | %s | %s | %s | %d | %s | %s |\n\n",
		report.Overall.Runs, FormatPercent(report.Overall.SuccessRate), report.Overall.MeanScore,
		ratio(report.Overall.UnsupportedClaims, report.Overall.ActiveClaims),
		ratio(report.Overall.IncorrectRetractions, report.Overall.RetractionsEvaluated),
		ratio(report.Overall.VerificationCorrect, report.Overall.VerificationCorrect+report.Overall.VerificationIncorrect),
		reproducibility(report.Overall), report.Overall.TotalTokens, cost(report.Overall), duration(report.Overall.MeanDurationMS))

	b.WriteString("## Metric definitions\n\n")
	b.WriteString("- **Success:** the domain adapter's evaluation passed. It is not the model's self-report.\n")
	b.WriteString("- **Unsupported claim rate:** active claims without a live chain to direct observations and verified assumptions, divided by active claims. The denominator is shown in the raw JSON.\n")
	b.WriteString("- **Incorrect retraction rate:** verification/contradiction-driven retractions that lack a matching counterexample observation, divided by the number of such retractions that can be assessed. Manual and consistency retractions are excluded.\n")
	b.WriteString("- **Verification accuracy:** verification records whose referenced evidence agrees with the outcome, divided by assessable verification records. Unknown records are reported separately and excluded.\n")
	b.WriteString("- **Reproducibility:** when a mode has multiple trials, the share of trials matching the most common canonical evidence signature. Signatures exclude timestamps and internal contradiction guards. With one trial the value is `n/a`.\n")
	b.WriteString("- **Cost:** only reported provider cost or configured per-token prices are summed. Missing cost is never estimated.\n\n")

	b.WriteString("## Run details\n\n")
	b.WriteString("| Run | Mode | Trial | Success | Score | Steps | Tokens | Duration | Error |\n")
	b.WriteString("|---|---|---|---:|---:|---:|---:|---:|---|\n")
	for _, record := range report.Runs {
		errorText := record.Error
		if errorText == "" {
			errorText = "—"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %d | %t | %.3f | %d | %d | %s | %s |\n",
			record.RunID, record.Mode, record.Trial, record.Success, record.Score,
			record.Metrics.Steps, record.Metrics.TotalTokens, duration(float64(record.Metrics.DurationMS)), escapeCell(errorText))
	}
	b.WriteString("\n")

	if len(report.Tasks) > 0 {
		b.WriteString("## Tasks\n\n")
		for _, task := range report.Tasks {
			fmt.Fprintf(&b, "- `%s` (%s): %s\n", task.ID, task.Domain, task.Prompt)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Notes and limitations\n\n")
	for _, note := range report.Notes {
		fmt.Fprintf(&b, "- %s\n", note)
	}
	b.WriteString("- The science tool is a fixed, validated Python script executed in a resource-limited subprocess. It is not a general-purpose code sandbox and must not be treated as a security boundary for hostile models.\n")
	b.WriteString("- Reproducibility signatures describe the recorded evidence state, not the scientific truth of the claims.\n")
	return b.String()
}

func ratio(numerator, denominator int) string {
	if denominator == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%s (%d/%d)", FormatPercent(float64(numerator)/float64(denominator)), numerator, denominator)
}

func reproducibility(aggregate Aggregate) string {
	if aggregate.ReproducibilityRuns == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%s (%d runs)", FormatPercent(aggregate.Reproducibility), aggregate.ReproducibilityRuns)
}

func cost(aggregate Aggregate) string {
	if aggregate.Runs == 0 {
		return "n/a"
	}
	if !aggregate.CostComplete {
		if aggregate.CostUSD > 0 {
			return fmt.Sprintf(">= $%.4f (partial)", aggregate.CostUSD)
		}
		return "n/a"
	}
	return fmt.Sprintf("$%.4f", aggregate.CostUSD)
}

func duration(milliseconds float64) string {
	if milliseconds <= 0 {
		return "n/a"
	}
	return time.Duration(milliseconds * float64(time.Millisecond)).Round(time.Millisecond).String()
}

func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

// SummaryRows is used by the CLI to render a compact table without parsing
// Markdown.
type SummaryRow struct {
	Mode          string
	Runs          int
	SuccessRate   float64
	MeanScore     float64
	Unsupported   string
	IncorrectRetr string
	Verification  string
	Reproducible  string
	Tokens        int
	Cost          string
	MeanTime      string
}

func SummaryRows(report *Report) []SummaryRow {
	if report == nil {
		return nil
	}
	rows := []SummaryRow{}
	for _, mode := range report.Modes {
		aggregate, ok := report.ByMode[string(mode)]
		if !ok {
			continue
		}
		rows = append(rows, SummaryRow{
			Mode: string(mode), Runs: aggregate.Runs,
			SuccessRate: aggregate.SuccessRate, MeanScore: aggregate.MeanScore,
			Unsupported:   ratio(aggregate.UnsupportedClaims, aggregate.ActiveClaims),
			IncorrectRetr: ratio(aggregate.IncorrectRetractions, aggregate.RetractionsEvaluated),
			Verification:  ratio(aggregate.VerificationCorrect, aggregate.VerificationCorrect+aggregate.VerificationIncorrect),
			Reproducible:  reproducibility(aggregate),
			Tokens:        aggregate.TotalTokens, Cost: cost(aggregate),
			MeanTime: duration(aggregate.MeanDurationMS),
		})
	}
	return rows
}

// SortRunIDs returns run IDs in a stable order for deterministic tooling.
func SortRunIDs(ids []string) {
	sort.Strings(ids)
}
