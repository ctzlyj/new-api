# Qiniu Peak Pricing Compatibility Design

## Problem

Qiniu changed some text-model pricing metadata from the legacy `cache`, `ncache`, and `output` meters to paired peak/off-peak meters such as `cache_peak` and `cache_offpeak`. The synchronizer treats every unknown meter as invalid pricing, so otherwise callable OpenAI-compatible text models are removed from the managed channel and lose their distributor route.

## Decision

For known token meters, billing uses the peak price as the conservative static price:

- `input_peak` and `ncache_peak` map to prompt tokens.
- `output_peak` maps to completion tokens.
- `cache_peak` maps to cached input tokens.
- Matching known `*_offpeak` meters are accepted as metadata but do not contribute to the expression.

The synchronizer continues to reject unknown meters, unsupported units, duplicate variables, and snapshots that contain only off-peak meters. This prevents an upstream schema change from silently underbilling traffic.

## Scope

Only Qiniu pricing-meter normalization and its focused regression tests change. The model sync schedule, resource-package conversion formula, channel configuration, user balances, historical billing records, and production deployment remain unchanged.

## Verification

A regression test reproduces the live six-meter schema for `deepseek/deepseek-v4-flash-20260731` and asserts the generated expression uses the three peak prices. A second assertion verifies an off-peak meter without its peak counterpart remains invalid. The focused service tests and root Go build must pass before commit.
