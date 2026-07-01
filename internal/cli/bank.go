package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/anjor/freeagent-cli/internal/config"
	"github.com/anjor/freeagent-cli/internal/freeagent"

	"github.com/urfave/cli/v2"
)

func bankCommand() *cli.Command {
	return &cli.Command{
		Name:  "bank",
		Usage: "Work with bank transactions",
		Subcommands: []*cli.Command{
			{
				Name:  "approve",
				Usage: "Approve bank transactions in bulk",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "bank-account", Usage: "Bank account ID or URL (required for date filters)"},
					&cli.StringFlag{Name: "from", Usage: "Start date (YYYY-MM-DD)"},
					&cli.StringFlag{Name: "to", Usage: "End date (YYYY-MM-DD)"},
					&cli.StringFlag{Name: "updated-since", Usage: "Updated since (YYYY-MM-DD)"},
					&cli.StringFlag{Name: "ids", Usage: "Comma list or file path with IDs/URLs"},
					&cli.StringFlag{Name: "ids-type", Value: "transaction", Usage: "ids type: transaction or explanation"},
				},
				Action: bankApprove,
			},
			explainSubcommand(),
		},
	}
}

func bankApprove(c *cli.Context) error {
	rt, err := runtimeFrom(c)
	if err != nil {
		return err
	}

	cfg, _, err := loadConfig(rt)
	if err != nil {
		return err
	}
	profile := ensureProfile(cfg, rt.Profile, rt, config.Profile{})

	client, _, err := newClient(context.Background(), rt, profile)
	if err != nil {
		return err
	}

	idsInput := strings.TrimSpace(c.String("ids"))
	idsType := strings.TrimSpace(strings.ToLower(c.String("ids-type")))
	if idsType == "" {
		idsType = "transaction"
	}
	if idsType != "transaction" && idsType != "explanation" {
		return fmt.Errorf("ids-type must be transaction or explanation")
	}

	var explanations []string
	if idsInput != "" {
		ids, err := parseIDList(idsInput)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return fmt.Errorf("no ids provided")
		}
		if idsType == "transaction" {
			explanations, err = explanationsForTransactions(c.Context, client, profile.BaseURL, ids)
			if err != nil {
				return err
			}
		} else {
			for _, id := range ids {
				resolved, err := normalizeResourceURL(profile.BaseURL, "bank_transaction_explanations", id)
				if err != nil {
					return err
				}
				explanations = append(explanations, resolved)
			}
		}
	} else {
		explanations, err = explanationsForDateRange(c, client, profile.BaseURL)
		if err != nil {
			return err
		}
	}

	explanations = dedupeStrings(explanations)
	if len(explanations) == 0 {
		return fmt.Errorf("no transactions to approve")
	}

	result := approveExplanations(c.Context, client, explanations)
	if rt.JSONOutput {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return writeJSONOutput(data)
	}

	if len(result.Failed) == 0 {
		fmt.Fprintf(os.Stdout, "Approved %d transaction(s)\n", len(result.Approved))
		return nil
	}

	fmt.Fprintf(os.Stdout, "Approved %d transaction(s), %d failed\n", len(result.Approved), len(result.Failed))
	for _, failure := range result.Failed {
		fmt.Fprintf(os.Stdout, "Failed: %s (%s)\n", failure.ID, failure.Error)
	}
	return fmt.Errorf("some approvals failed")
}

type approveResult struct {
	Approved []string        `json:"approved"`
	Failed   []approveFailed `json:"failed"`
}

type approveFailed struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

func approveExplanations(ctx context.Context, client *freeagent.Client, explanations []string) approveResult {
	result := approveResult{}
	payload := map[string]any{
		"bank_transaction_explanation": map[string]any{
			"marked_for_review": false,
		},
	}
	for _, explanation := range explanations {
		_, _, _, err := client.DoJSON(ctx, http.MethodPut, explanation, payload)
		if err != nil {
			result.Failed = append(result.Failed, approveFailed{ID: explanation, Error: err.Error()})
			continue
		}
		result.Approved = append(result.Approved, explanation)
	}
	return result
}

func explanationsForDateRange(c *cli.Context, client *freeagent.Client, baseURL string) ([]string, error) {
	bankAccount := strings.TrimSpace(c.String("bank-account"))
	if bankAccount == "" {
		return nil, fmt.Errorf("bank-account is required when approving by date range")
	}
	bankAccountURL, err := normalizeResourceURL(baseURL, "bank_accounts", bankAccount)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("bank_account", bankAccountURL)
	if v := strings.TrimSpace(c.String("from")); v != "" {
		query.Set("from_date", v)
	}
	if v := strings.TrimSpace(c.String("to")); v != "" {
		query.Set("to_date", v)
	}
	if v := strings.TrimSpace(c.String("updated-since")); v != "" {
		query.Set("updated_since", v)
	}

	if query.Get("from_date") == "" && query.Get("to_date") == "" && query.Get("updated_since") == "" {
		return nil, fmt.Errorf("provide --ids or a date filter (--from/--to/--updated-since)")
	}

	path := "/bank_transaction_explanations?" + query.Encode()
	resp, _, _, err := client.Do(context.Background(), http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}

	var decoded map[string]any
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, err
	}
	list, _ := decoded["bank_transaction_explanations"].([]any)

	var explanations []string
	for _, item := range list {
		explanation, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if marked, ok := explanation["marked_for_review"].(bool); ok && !marked {
			continue
		}
		if urlValue, ok := explanation["url"].(string); ok && urlValue != "" {
			explanations = append(explanations, urlValue)
		}
	}
	return explanations, nil
}

func explanationsForTransactions(ctx context.Context, client *freeagent.Client, baseURL string, ids []string) ([]string, error) {
	var explanations []string
	for _, id := range ids {
		txnURL, err := normalizeResourceURL(baseURL, "bank_transactions", id)
		if err != nil {
			return nil, err
		}
		resp, _, _, err := client.Do(ctx, http.MethodGet, txnURL, nil, "")
		if err != nil {
			return nil, err
		}
		var decoded map[string]any
		if err := json.Unmarshal(resp, &decoded); err != nil {
			return nil, err
		}
		txn, _ := decoded["bank_transaction"].(map[string]any)
		if txn == nil {
			continue
		}
		items, _ := txn["bank_transaction_explanations"].([]any)
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if urlValue, ok := entry["url"].(string); ok && urlValue != "" {
				explanations = append(explanations, urlValue)
			}
		}
	}
	return explanations, nil
}

func parseIDList(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}
	if strings.HasPrefix(input, "@") {
		path := strings.TrimPrefix(input, "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return splitIDs(string(data)), nil
	}
	if looksLikeFile(input) {
		data, err := os.ReadFile(input)
		if err != nil {
			return nil, err
		}
		return splitIDs(string(data)), nil
	}
	return splitIDs(input), nil
}

func looksLikeFile(path string) bool {
	if strings.Contains(path, "\n") {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

func splitIDs(input string) []string {
	input = strings.TrimSpace(strings.TrimPrefix(input, "@"))
	if input == "" {
		return nil
	}
	raw := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	var out []string
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
