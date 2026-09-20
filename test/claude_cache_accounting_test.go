package test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pricing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	statistics "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestClaudeCacheAccountingAcrossBoundaries(t *testing.T) {
	previous := statistics.StatisticsEnabled()
	statistics.SetStatisticsEnabled(true)
	t.Cleanup(func() { statistics.SetStatisticsEnabled(previous) })
	for _, provider := range []string{"openai", "codex"} {
		for _, tc := range []struct {
			name, cache                   string
			input, wantInput, read, write int64
			valid                         bool
		}{
			{"write", `"cached_tokens":800,"cache_write_tokens":150`, 1000, 50, 800, 150, true},
			{"creation alias", `"cached_tokens":800,"cache_creation_tokens":150`, 1000, 50, 800, 150, true},
			{"canonical priority", `"cached_tokens":800,"cache_write_tokens":150,"cache_creation_tokens":90`, 1000, 50, 800, 150, true},
			{"zero fallback", `"cached_tokens":800,"cache_write_tokens":0,"cache_creation_tokens":150`, 1000, 50, 800, 150, true},
			{"negative fallback", `"cached_tokens":800,"cache_write_tokens":-1,"cache_creation_tokens":150`, 1000, 50, 800, 150, true},
			{"overflow", `"cached_tokens":9223372036854775807,"cache_write_tokens":9223372036854775807`, 1000, 0, math.MaxInt64, math.MaxInt64, false},
			{"negative input", `"cached_tokens":0`, -1, 0, 0, 0, false},
			{"negative caches", `"cached_tokens":-1,"cache_creation_tokens":-1`, 1000, 1000, 0, 0, false},
			{"cache exceeds input", `"cached_tokens":800,"cache_write_tokens":300`, 1000, 0, 800, 300, false},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				prefix, output := "prompt_tokens", "completion_tokens"
				if provider == "codex" {
					prefix, output = "input_tokens", "output_tokens"
				}
				usage := fmt.Sprintf(`{"%s":%d,"%s":200,"%s_details":{%s}}`, prefix, tc.input, output, prefix, tc.cache)
				raw := []byte(fmt.Sprintf(`{"id":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":%s}`, usage))
				detail := helps.ParseOpenAIUsage(raw)
				var nonstream []byte
				var chunks [][]byte
				var param any
				if provider == "codex" {
					raw = []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"test","usage":%s,"output":[]}}`, usage))
					var ok bool
					detail, ok = helps.ParseCodexUsage(raw)
					if !ok {
						t.Fatal("missing usage")
					}
					nonstream = codexclaude.ConvertCodexResponseToClaudeNonStream(ctx, "", nil, nil, raw, nil)
					chunks = codexclaude.ConvertCodexResponseToClaude(ctx, "", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"test"}}`), &param)
					chunks = append(chunks, codexclaude.ConvertCodexResponseToClaude(ctx, "", nil, nil, append([]byte("data: "), raw...), &param)...)
				} else {
					nonstream = openaiclaude.ConvertOpenAIResponseToClaudeNonStream(ctx, "", nil, nil, raw, nil)
					stream := []byte(fmt.Sprintf(`data: {"id":"test","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":%s}`, usage))
					chunks = openaiclaude.ConvertOpenAIResponseToClaude(ctx, "", []byte(`{"stream":true}`), nil, stream, &param)
					// The same entrypoint also accepts a complete non-stream response.
					direct := openaiclaude.ConvertOpenAIResponseToClaude(ctx, "", nil, nil, append([]byte("data: "), raw...), new(any))
					if len(direct) != 1 {
						t.Fatalf("direct response: %q", direct)
					}
					if gjson.GetBytes(direct[0], "usage").Raw != gjson.GetBytes(nonstream, "usage").Raw {
						t.Fatal("non-stream entrypoints disagree")
					}
				}
				responses := [][]byte{nonstream}
				for _, chunk := range chunks {
					for _, line := range strings.Split(string(chunk), "\n") {
						if strings.HasPrefix(line, "data: ") && gjson.Get(strings.TrimPrefix(line, "data: "), "type").String() == "message_delta" {
							responses = append(responses, []byte(strings.TrimPrefix(line, "data: ")))
						}
					}
				}
				if len(responses) != 2 {
					t.Fatalf("missing terminal usage: %q", chunks)
				}
				record := coreusage.Record{Provider: provider, Model: "gpt-5.6", Detail: detail}
				bill := pricing.NewService().CalculateUsageCost(record)
				if !tc.valid {
					if detail.TokenBreakdown.Quality != coreusage.TokenAccountingQualityInconsistent || bill.Priced {
						t.Fatalf("invalid source was billable: %+v %+v", detail, bill)
					}
				}
				for _, response := range responses {
					u := gjson.GetBytes(response, "usage")
					if u.Get("input_tokens").Int() != tc.wantInput || u.Get("cache_read_input_tokens").Int() != tc.read || u.Get("cache_creation_input_tokens").Int() != tc.write || u.Get("output_tokens").Int() != 200 {
						t.Fatalf("unexpected Claude usage: %s", u.Raw)
					}
					if tc.valid {
						parsed := helps.ParseClaudeUsage([]byte(`{"usage":` + u.Raw + `}`))
						if parsed.TokenBreakdown != detail.TokenBreakdown {
							t.Fatalf("roundtrip accounting differs: %+v vs %+v", parsed.TokenBreakdown, detail.TokenBreakdown)
						}
					}
				}
				if !tc.valid {
					return
				}
				if !bill.Priced || math.Abs(bill.TotalUSD-0.0075875) > 1e-12 {
					t.Fatalf("unexpected cost: %+v", bill)
				}
				record.Billing = bill
				stats := statistics.NewRequestStatistics()
				stats.Record(ctx, record)
				snapshot := stats.Snapshot()
				if snapshot.TotalTokens != 1200 || snapshot.Tokens.InputTokens != 50 || snapshot.Tokens.CacheReadTokens != 800 || snapshot.Tokens.CacheWriteTokens != 150 || snapshot.TotalCostUSD != bill.TotalUSD {
					t.Fatalf("statistics double count: %+v", snapshot)
				}
				page := stats.QueryRecords(statistics.UsageQuery{}, 1, 10, "", "asc")
				if len(page.Items) != 1 {
					t.Fatalf("missing management detail: %+v", page)
				}
				if page.Items[0].Tokens != snapshot.Tokens {
					t.Fatalf("management detail differs: %+v", page.Items[0])
				}
			})
		}
	}
}
