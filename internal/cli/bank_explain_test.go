package cli

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectContentType(t *testing.T) {
	cases := []struct {
		name     string
		fileName string
		want     string
		wantErr  bool
	}{
		{name: "png", fileName: "receipt.png", want: "image/png"},
		{name: "png uppercase extension", fileName: "RECEIPT.PNG", want: "image/png"},
		{name: "jpg", fileName: "receipt.jpg", want: "image/jpeg"},
		{name: "jpeg", fileName: "receipt.jpeg", want: "image/jpeg"},
		{name: "gif", fileName: "receipt.gif", want: "image/gif"},
		{name: "pdf uses freeagent x-pdf", fileName: "receipt.pdf", want: "application/x-pdf"},
		{name: "unsupported extension", fileName: "receipt.txt", wantErr: true},
		{name: "no extension", fileName: "receipt", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectContentType(tc.fileName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got none", tc.fileName)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("detectContentType(%q) = %q, want %q", tc.fileName, got, tc.want)
			}
		})
	}
}

func TestValidateContentType(t *testing.T) {
	valid := []string{"image/png", "image/x-png", "image/jpeg", "image/jpg", "image/gif", "application/x-pdf"}
	for _, ct := range valid {
		if err := validateContentType(ct); err != nil {
			t.Errorf("validateContentType(%q) returned error: %v", ct, err)
		}
	}

	invalid := []string{"application/pdf", "text/plain", "image/webp", ""}
	for _, ct := range invalid {
		if err := validateContentType(ct); err == nil {
			t.Errorf("validateContentType(%q) expected error, got none", ct)
		}
	}
}

func TestBuildAttachmentHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "receipt.png")
	content := []byte("fake-png-bytes")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	att, err := buildAttachment(path, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if att.FileName != "receipt.png" {
		t.Errorf("FileName = %q, want %q", att.FileName, "receipt.png")
	}
	if att.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want %q", att.ContentType, "image/png")
	}
	wantData := base64.StdEncoding.EncodeToString(content)
	if att.Data != wantData {
		t.Errorf("Data = %q, want base64 %q", att.Data, wantData)
	}
}

func TestBuildAttachmentContentTypeOverride(t *testing.T) {
	dir := t.TempDir()
	// extension the detector would reject, proving the override is used.
	path := filepath.Join(dir, "scan.bin")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	att, err := buildAttachment(path, "image/x-png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if att.ContentType != "image/x-png" {
		t.Errorf("ContentType = %q, want %q", att.ContentType, "image/x-png")
	}
}

func TestBuildAttachmentErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		if _, err := buildAttachment(filepath.Join(dir, "nope.png"), ""); err == nil {
			t.Fatal("expected error for missing file, got none")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		path := filepath.Join(dir, "empty.png")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		if _, err := buildAttachment(path, ""); err == nil {
			t.Fatal("expected error for empty file, got none")
		}
	})

	t.Run("unsupported extension without override", func(t *testing.T) {
		path := filepath.Join(dir, "notes.txt")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		if _, err := buildAttachment(path, ""); err == nil {
			t.Fatal("expected error for unsupported extension, got none")
		}
	})

	t.Run("invalid content type override", func(t *testing.T) {
		path := filepath.Join(dir, "receipt.png")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		if _, err := buildAttachment(path, "application/pdf"); err == nil {
			t.Fatal("expected error for invalid content type override, got none")
		}
	})

	t.Run("exceeds 5MB limit", func(t *testing.T) {
		path := filepath.Join(dir, "big.png")
		if err := os.WriteFile(path, make([]byte, maxAttachmentBytes+1), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		if _, err := buildAttachment(path, ""); err == nil {
			t.Fatal("expected error for oversized file, got none")
		}
	})
}

func TestValidateExplanationFlags(t *testing.T) {
	if err := validateExplanationFlags("txn-1", "2026-06-02", "-6.17"); err != nil {
		t.Fatalf("expected no error for complete flags, got %v", err)
	}

	cases := []struct {
		name        string
		transaction string
		datedOn     string
		grossValue  string
		wantMissing string
	}{
		{name: "missing transaction", datedOn: "2026-06-02", grossValue: "-6.17", wantMissing: "--transaction"},
		{name: "missing dated-on", transaction: "txn-1", grossValue: "-6.17", wantMissing: "--dated-on"},
		{name: "missing gross-value", transaction: "txn-1", datedOn: "2026-06-02", wantMissing: "--gross-value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExplanationFlags(tc.transaction, tc.datedOn, tc.grossValue)
			if err == nil {
				t.Fatalf("expected error, got none")
			}
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantMissing)
			}
		})
	}
}

func TestValidateDate(t *testing.T) {
	if err := validateDate("2026-06-02"); err != nil {
		t.Errorf("expected valid date, got error: %v", err)
	}
	for _, bad := range []string{"02/06/2026", "2026-13-40", "june", ""} {
		if err := validateDate(bad); err == nil {
			t.Errorf("expected error for %q, got none", bad)
		}
	}
}

func TestBuildExplanationRequestMinimal(t *testing.T) {
	req := buildExplanationRequest(explanationInput{
		Transaction: "https://api.freeagent.com/v2/bank_transactions/1",
		DatedOn:     "2026-06-02",
		GrossValue:  "-6.17",
	})

	body := marshalToMap(t, req)
	explanation, ok := body["bank_transaction_explanation"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing bank_transaction_explanation wrapper: %v", body)
	}

	if explanation["bank_transaction"] != "https://api.freeagent.com/v2/bank_transactions/1" {
		t.Errorf("bank_transaction = %v", explanation["bank_transaction"])
	}
	if explanation["dated_on"] != "2026-06-02" {
		t.Errorf("dated_on = %v", explanation["dated_on"])
	}
	if explanation["gross_value"] != "-6.17" {
		t.Errorf("gross_value = %v", explanation["gross_value"])
	}
	for _, omitted := range []string{"category", "description", "attachment", "bank_account"} {
		if _, present := explanation[omitted]; present {
			t.Errorf("expected %q to be omitted when empty", omitted)
		}
	}
}

func TestBuildExplanationRequestFull(t *testing.T) {
	req := buildExplanationRequest(explanationInput{
		Transaction: "https://api.freeagent.com/v2/bank_transactions/1",
		DatedOn:     "2026-06-02",
		GrossValue:  "-6.17",
		Category:    "https://api.freeagent.com/v2/categories/285",
		Description: "AWS EMEA",
		Attachment: &attachmentPayload{
			Data:        "YmFzZTY0",
			FileName:    "aws.pdf",
			ContentType: "application/x-pdf",
		},
	})

	body := marshalToMap(t, req)
	explanation := body["bank_transaction_explanation"].(map[string]any)

	if explanation["category"] != "https://api.freeagent.com/v2/categories/285" {
		t.Errorf("category = %v", explanation["category"])
	}
	if explanation["description"] != "AWS EMEA" {
		t.Errorf("description = %v", explanation["description"])
	}

	attachment, ok := explanation["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("attachment missing or wrong type: %v", explanation["attachment"])
	}
	if attachment["data"] != "YmFzZTY0" {
		t.Errorf("attachment data = %v", attachment["data"])
	}
	if attachment["file_name"] != "aws.pdf" {
		t.Errorf("attachment file_name = %v", attachment["file_name"])
	}
	if attachment["content_type"] != "application/x-pdf" {
		t.Errorf("attachment content_type = %v", attachment["content_type"])
	}
}

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
