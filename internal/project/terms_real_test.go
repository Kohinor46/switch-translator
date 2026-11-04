package project

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"switch-translator/internal/gametext"
)

// SWTR_STRINGS=strings.USen-ru.json go test -run TestTermsReal -v
func TestTermsReal(t *testing.T) {
	p := os.Getenv("SWTR_STRINGS")
	if p == "" {
		t.Skip("SWTR_STRINGS не задан")
	}
	b, _ := os.ReadFile(p)
	var strs []gametext.String
	json.Unmarshal(b, &strs)
	var texts []string
	for _, s := range strs {
		if !s.Skip {
			texts = append(texts, s.Source)
		}
	}
	terms, st := CollectTerms(texts)
	fmt.Printf("строк %d, слов %d, разных %d, терминов %d\n", len(texts), st.Words, st.Unique, len(terms))
	for i, tm := range terms {
		if i < 80 || i%40 == 0 {
			fmt.Printf("%5d  %-30s  %s\n", tm.Count, tm.Source, tm.Example)
		}
	}
}
