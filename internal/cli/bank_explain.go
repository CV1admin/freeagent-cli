package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anjor/freeagent-cli/internal/config"

	"github.com/urfave/cli/v2"
)

// freeagent caps attachments at 5MB.
const maxAttachmentBytes = 5 * 1024 * 1024

// content types the freeagent api accepts (note application/x-pdf, not application/pdf).
var allowedContentTypes = map[string]struct{}{
	"image/png":         {},
	"image/x-png":       {},
	"image/jpeg":        {},
	"image/jpg":         {},
	"image/gif":         {},
	"application/x-pdf": {},
}

// attachmentPayload's data must be base64-encoded, per the freeagent api.
type attachmentPayload struct {
	Data        string `json:"data"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Description string `json:"description,omitempty"`
}

type explanationPayload struct {
	BankTransaction string             `json:"bank_transaction"`
	DatedOn         string             `json:"dated_on"`
	GrossValue      string             `json:"gross_value"`
	Category        string             `json:"category,omitempty"`
	Description     string             `json:"description,omitempty"`
	Attachment      *attachmentPayload `json:"attachment,omitempty"`
}

type explanationRequest struct {
	BankTransactionExplanation explanationPayload `json:"bank_transaction_explanation"`
}

// explanationInput holds already-normalized, validated values (not raw flags).
type explanationInput struct {
	Transaction string
	DatedOn     string
	GrossValue  string
	Category    string
	Description string
	Attachment  *attachmentPayload
}

func explainSubcommand() *cli.Command {
	return &cli.Command{
		Name:  "explain",
		Usage: "Explain (categorize) a bank transaction and optionally attach a receipt",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "transaction", Usage: "Bank transaction ID or URL to explain (required)"},
			&cli.StringFlag{Name: "dated-on", Usage: "Explanation date, YYYY-MM-DD (required)"},
			&cli.StringFlag{Name: "gross-value", Usage: "Gross value in the account currency, e.g. -6.17 (required)"},
			&cli.StringFlag{Name: "category", Usage: "Category ID or URL"},
			&cli.StringFlag{Name: "description", Usage: "Explanation description"},
			&cli.StringFlag{Name: "attach", Usage: "Path to a receipt to attach (png/jpg/jpeg/gif/pdf, max 5MB)"},
			&cli.StringFlag{Name: "content-type", Usage: "Override attachment content type (one of image/png, image/x-png, image/jpeg, image/jpg, image/gif, application/x-pdf)"},
		},
		Action: bankExplain,
	}
}

func bankExplain(c *cli.Context) error {
	rt, err := runtimeFrom(c)
	if err != nil {
		return err
	}

	cfg, _, err := loadConfig(rt)
	if err != nil {
		return err
	}
	profile := ensureProfile(cfg, rt.Profile, rt, config.Profile{})

	client, _, err := newClient(c.Context, rt, profile)
	if err != nil {
		return err
	}

	transaction := strings.TrimSpace(c.String("transaction"))
	datedOn := strings.TrimSpace(c.String("dated-on"))
	grossValue := strings.TrimSpace(c.String("gross-value"))
	category := strings.TrimSpace(c.String("category"))
	description := c.String("description")
	attachPath := strings.TrimSpace(c.String("attach"))
	contentType := strings.TrimSpace(c.String("content-type"))

	if err := validateExplanationFlags(transaction, datedOn, grossValue); err != nil {
		return err
	}
	if err := validateDate(datedOn); err != nil {
		return err
	}
	if attachPath == "" && contentType != "" {
		return fmt.Errorf("--content-type is only valid together with --attach")
	}

	transactionURL, err := normalizeResourceURL(profile.BaseURL, "bank_transactions", transaction)
	if err != nil {
		return err
	}

	input := explanationInput{
		Transaction: transactionURL,
		DatedOn:     datedOn,
		GrossValue:  grossValue,
		Description: description,
	}

	if category != "" {
		categoryURL, err := normalizeResourceURL(profile.BaseURL, "categories", category)
		if err != nil {
			return err
		}
		input.Category = categoryURL
	}

	if attachPath != "" {
		attachment, err := buildAttachment(attachPath, contentType)
		if err != nil {
			return err
		}
		input.Attachment = attachment
	}

	request := buildExplanationRequest(input)

	resp, status, _, err := client.DoJSON(c.Context, http.MethodPost, "/bank_transaction_explanations", request)
	if err != nil {
		return err
	}

	if rt.JSONOutput {
		return writeJSONOutput(resp)
	}

	return printExplanationResult(os.Stdout, resp, status)
}

func validateExplanationFlags(transaction, datedOn, grossValue string) error {
	var missing []string
	if strings.TrimSpace(transaction) == "" {
		missing = append(missing, "--transaction")
	}
	if strings.TrimSpace(datedOn) == "" {
		missing = append(missing, "--dated-on")
	}
	if strings.TrimSpace(grossValue) == "" {
		missing = append(missing, "--gross-value")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flag(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

func validateDate(datedOn string) error {
	if _, err := time.Parse("2006-01-02", strings.TrimSpace(datedOn)); err != nil {
		return fmt.Errorf("dated-on must be a valid date in YYYY-MM-DD format: %q", datedOn)
	}
	return nil
}

func detectContentType(fileName string) (string, error) {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".png":
		return "image/png", nil
	case ".jpg", ".jpeg":
		return "image/jpeg", nil
	case ".gif":
		return "image/gif", nil
	case ".pdf":
		return "application/x-pdf", nil
	default:
		return "", fmt.Errorf("unsupported attachment type %q (allowed: .png, .jpg, .jpeg, .gif, .pdf)", filepath.Ext(fileName))
	}
}

func validateContentType(contentType string) error {
	if _, ok := allowedContentTypes[contentType]; !ok {
		return fmt.Errorf("unsupported content type %q (allowed: image/png, image/x-png, image/jpeg, image/jpg, image/gif, application/x-pdf)", contentType)
	}
	return nil
}

// contentTypeOverride, when set, takes precedence over extension-based detection.
func buildAttachment(filePath, contentTypeOverride string) (*attachmentPayload, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("attachment file %q is empty", filePath)
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("attachment %q is %d bytes, which exceeds the 5MB limit", filePath, len(data))
	}

	contentType := strings.TrimSpace(contentTypeOverride)
	if contentType == "" {
		contentType, err = detectContentType(filePath)
		if err != nil {
			return nil, err
		}
	} else if err := validateContentType(contentType); err != nil {
		return nil, err
	}

	return &attachmentPayload{
		Data:        base64.StdEncoding.EncodeToString(data),
		FileName:    filepath.Base(filePath),
		ContentType: contentType,
	}, nil
}

func buildExplanationRequest(in explanationInput) explanationRequest {
	return explanationRequest{
		BankTransactionExplanation: explanationPayload{
			BankTransaction: in.Transaction,
			DatedOn:         in.DatedOn,
			GrossValue:      in.GrossValue,
			Category:        in.Category,
			Description:     strings.TrimSpace(in.Description),
			Attachment:      in.Attachment,
		},
	}
}

func printExplanationResult(w io.Writer, resp []byte, status int) error {
	var decoded struct {
		BankTransactionExplanation struct {
			URL string `json:"url"`
		} `json:"bank_transaction_explanation"`
	}
	if err := json.Unmarshal(resp, &decoded); err == nil && decoded.BankTransactionExplanation.URL != "" {
		fmt.Fprintf(w, "Explained bank transaction. Explanation: %s\n", decoded.BankTransactionExplanation.URL)
		return nil
	}

	_, err := w.Write(append(resp, '\n'))
	return err
}
