# Modelink Dual Catalog Synchronization Design

## Problem

The production API key is valid on both provider hosts, but the current synchronizer only reads `https://api.qnaigc.com/v1/models` and the Qiniu marketplace. The same key currently returns 76 Qiniu models and 139 Modelink models. Seventy-five IDs overlap, 64 IDs are Modelink-only, and the Modelink public page contains metadata for all 139 callable IDs.

The existing `SF OpenAI Upstream` channel already uses `https://api.modelink.ai`, but it intentionally contains only the manually priced `SF-gpt-image-2`. Expanding or replacing that channel would risk deleting its special billing expression.

## Decision

Keep three independent routes:

- `qiniu-managed`: Qiniu callable models, using `https://api.qnaigc.com`.
- `modelink-managed`: Modelink-only callable models, using `https://api.modelink.ai` and the same API key.
- `sufy-upstream`: the existing manual `SF-gpt-image-2` route, unchanged.

The 75 overlapping IDs stay on the Qiniu route. Modelink receives only IDs not returned by Qiniu, preventing duplicate distributors and avoiding conflicting global model prices. Both managed route snapshots are applied in one database transaction so a model can move between sources without a partial outage.

## Catalog And Pricing

The Modelink marketplace is parsed from the Next.js flight payload on `https://modelink.ai/zh-CN/models`. Its camel-case schema is normalized into the existing Qiniu marketplace model representation so the same admission, icon, vendor, tags, pricing conversion, and billing-expression validation remain authoritative.

Supported Modelink token meters map as follows:

- `input`, `ncache`, `t_input`, `nth_input`, `th_input` -> prompt tokens.
- `output`, `t_output`, `nth_output`, `th_output` -> completion tokens.
- `cache`, `ex_cache` -> cache-read tokens.
- `c_cache` -> cache-creation tokens.
- `c_1h_cache` -> one-hour cache-creation tokens.
- `a_input` -> audio-input tokens.
- `i_output` -> image-output tokens.
- Peak/off-peak pairs continue using the peak price.
- Batch meters are ignored because the managed channels expose synchronous relay APIs, not the batch API.
- `web_search_req` is ignored in the token expression because the existing tool-surcharge path already bills Claude web search at USD 10/1K calls and Gemini Google search at USD 14/1K calls, matching Modelink's CNY 0.069 and CNY 0.0966 per call.

The resource-package sale conversion remains unchanged: cost CNY 323 and sale CNY 350 per 100M deduction tokens, with 20 points per CNY. Missing or invalid pricing is never guessed. At the current live snapshot, `nvidia/nemotron-3-super-120b-a12b` is callable but has no public price, so it remains hidden until metadata appears.

## Metadata And Routing

Modelink models use the existing trusted Qiniu icon CDN allow-list and receive a `Modelink` source tag plus vendor, capability, modality, hot, and retirement tags. Models with image output remain OpenAI-compatible catalog entries; this change does not claim unverified `/v1/images/*` endpoint support.

The managed model metadata owner remains the existing `qiniu-managed` marker, while channel tags identify route ownership. This avoids migrating production metadata ownership and still lets one atomic synchronizer hide, restore, or move models across the two managed channels.

## Safety

- Never log or return API keys or authorization headers.
- Reject cross-host redirects and oversized marketplace responses.
- Refuse empty source snapshots.
- Apply both managed channels, model metadata, abilities, and billing options in one transaction.
- Do not update balances, redemption codes, historical billing logs, or `SF-gpt-image-2` pricing.
- Do not commit, push, or deploy without separate authorization.

## Expected Result

With the current provider snapshots, the managed catalogs should contain 76 Qiniu routes and 63 Modelink-only priced routes. Together with `SF-gpt-image-2`, the public marketplace should contain 140 models. The one callable Modelink model without pricing remains hidden rather than becoming free or underbilled.
