package invoice

import (
	"encoding/json"
	"testing"
)

// TestBank_RequiresName confirms schemas/invoice.json's bank.name
// requirement (added alongside iban/bic) actually made it through
// `go generate` into the generated validator.
func TestBank_RequiresName(t *testing.T) {
	t.Run("rejects missing name", func(t *testing.T) {
		var b Bank
		err := json.Unmarshal([]byte(`{"iban":"LT27...","bic":"REVOLT21"}`), &b)
		if err == nil {
			t.Fatal("expected error for bank missing name, got nil")
		}
	})

	t.Run("accepts full bank", func(t *testing.T) {
		var b Bank
		err := json.Unmarshal([]byte(`{"name":"Revolut Bank UAB","iban":"LT27...","bic":"REVOLT21"}`), &b)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Name != "Revolut Bank UAB" {
			t.Errorf("Name = %q, want %q", b.Name, "Revolut Bank UAB")
		}
	})
}
