# Claude cache accounting

This selectively ports upstream `883660fb8153f48cf514de3e9c8268d847afcc64`.

OpenAI Chat Completions and Codex Responses include cache reads and writes in
their input total. Claude reports uncached input, cache reads, and cache creation
as separate buckets. Both streaming and non-streaming conversion therefore emit:

- `input_tokens = max(0, upstream input - positive cache read - positive cache write)`
- `cache_read_input_tokens` for positive cache reads
- `cache_creation_input_tokens` for positive cache writes

A positive `cache_write_tokens` takes precedence over `cache_creation_tokens`.
A missing, zero, or negative canonical value falls back to the creation alias.
Local upstream usage parsing follows the same positive-alias fallback. Negative
source counts without a valid alias remain inconsistent for local accounting;
they are not silently made billable. Conversion omits negative cache fields and
clamps negative input to zero. Sequential subtraction avoids an overflowing cache
sum. Accounting construction and validation also use checked sums.

## Verified boundaries

Before this change, Codex emitted cache creation while leaving those tokens in
Claude input, so downstream independent-bucket billing could count writes twice.
The local OpenAI converter did not emit cache creation at all; it classified
writes as ordinary input. Its cache-creation output is included in this port.

Local executors report upstream usage, not the translated Claude usage.
`NewSubsetTokenBreakdown` already partitions valid upstream input into uncached,
read, and write buckets. Pricing charges each bucket once; management statistics
and request details consume that canonical breakdown. The frontend uses canonical
input totals and keeps cache sub-buckets separate. No frontend change or
historical-record rewrite is required.

For input 1000, read 800, write 150, and output 200, every conversion path now
reports input 50, read 800, creation 150, output 200. Local statistics retain total
1200. Regression coverage checks both aliases, precedence, zero/negative fallback,
negative input/cache values, cache exceeding input, and int64 overflow across
streaming/non-streaming conversion, parsing, pricing, and management records.
Invalid source accounting remains unpriced. Translator unit tests additionally
cover absent/null usage and cache-write-only responses.

Release publication does not update a running deployment or recompute past bills.
