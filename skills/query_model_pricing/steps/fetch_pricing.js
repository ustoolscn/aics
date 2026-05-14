const fs = require("fs");

const PRICING_URL = "https://cooper-api.com/api/pricing";

function readPayload() {
  const raw = fs.readFileSync(0, "utf8");
  return raw ? JSON.parse(raw) : {};
}

function numberValue(value, fallback = 0) {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function normalizeText(value) {
  return String(value || "").trim().toLowerCase();
}

function pickEffectiveGroup(model, requestedGroup, root) {
  const enableGroups = Array.isArray(model.enable_groups) ? model.enable_groups : [];
  const groupRatio = root.group_ratio || {};
  const requested = requestedGroup || "auto";

  if (requested === "auto") {
    for (const group of root.auto_groups || []) {
      if (enableGroups.includes(group)) {
        return {
          requested_group: "auto",
          effective_group: group,
          group_ratio: numberValue(groupRatio[group], 1),
          group_description: (root.usable_group || {})[group] || ""
        };
      }
    }
    return {
      requested_group: "auto",
      effective_group: "default",
      group_ratio: numberValue(groupRatio.default, 1),
      group_description: (root.usable_group || {}).default || ""
    };
  }

  return {
    requested_group: requested,
    effective_group: requested,
    group_ratio: numberValue(groupRatio[requested], 1),
    group_description: (root.usable_group || {})[requested] || "",
    group_available_for_model: enableGroups.length === 0 || enableGroups.includes(requested)
  };
}

function parseBillingExpr(expr, groupRatio) {
  const tiers = [];
  if (!expr) {
    return tiers;
  }

  const tierPattern = /tier\("([^"]+)"\s*,\s*([^)]*)\)/g;
  let match;
  while ((match = tierPattern.exec(expr)) !== null) {
    const name = match[1];
    const body = match[2];
    const input = extractCoefficient(body, "p") * groupRatio;
    const output = extractCoefficient(body, "c") * groupRatio;
    const cache = extractCoefficient(body, "cr") * groupRatio;
    tiers.push({
      name,
      input_per_million_tokens_usd: round(input),
      output_per_million_tokens_usd: round(output),
      cache_input_per_million_tokens_usd: round(cache)
    });
  }

  const conditions = [...expr.matchAll(/([^?:]+)\?\s*tier\("([^"]+)"/g)].map((item) => ({
    tier: item[2],
    condition: item[1].trim()
  }));
  for (const tier of tiers) {
    const condition = conditions.find((item) => item.tier === tier.name);
    if (condition) {
      tier.condition = condition.condition;
    }
  }
  return tiers;
}

function extractCoefficient(expr, variable) {
  const escaped = variable.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const patterns = [
    new RegExp(`${escaped}\\s*\\*\\s*([0-9.]+)`),
    new RegExp(`([0-9.]+)\\s*\\*\\s*${escaped}`)
  ];
  for (const pattern of patterns) {
    const match = expr.match(pattern);
    if (match) {
      return numberValue(match[1], 0);
    }
  }
  return 0;
}

function calculatePricing(model, groupInfo) {
  const groupRatio = groupInfo.group_ratio;
  if (numberValue(model.quota_type) === 1) {
    return {
      billing_type: "per_request",
      price_per_request_usd: round(numberValue(model.model_price) * groupRatio)
    };
  }

  if (model.billing_expr) {
    return {
      billing_type: "tiered_per_million_tokens",
      billing_mode: model.billing_mode || "tiered_expr",
      billing_expr: model.billing_expr,
      tiers: parseBillingExpr(model.billing_expr, groupRatio)
    };
  }

  const inputPrice = numberValue(model.model_ratio) * 2 * groupRatio;
  return {
    billing_type: "per_million_tokens",
    input_per_million_tokens_usd: round(inputPrice),
    output_per_million_tokens_usd: round(inputPrice * numberValue(model.completion_ratio, 1)),
    cache_input_per_million_tokens_usd: round(inputPrice * numberValue(model.cache_ratio, 0))
  };
}

function scoreForCheapest(pricing) {
  if (pricing.billing_type === "per_request") {
    return pricing.price_per_request_usd;
  }
  if (pricing.billing_type === "tiered_per_million_tokens") {
    const first = pricing.tiers && pricing.tiers[0];
    if (!first) return Number.POSITIVE_INFINITY;
    return first.input_per_million_tokens_usd + first.output_per_million_tokens_usd;
  }
  return pricing.input_per_million_tokens_usd + pricing.output_per_million_tokens_usd;
}

function round(value) {
  return Math.round(numberValue(value) * 1_000_000) / 1_000_000;
}

function summarizeModel(model, root, input) {
  const groupInfo = pickEffectiveGroup(model, input.group || "auto", root);
  const pricing = calculatePricing(model, groupInfo);
  return {
    model_name: model.model_name,
    description: model.description || "",
    tags: model.tags || "",
    quota_type: model.quota_type,
    supported_endpoint_types: model.supported_endpoint_types || [],
    enable_groups: model.enable_groups || [],
    group: groupInfo,
    pricing,
    cheapest_score: scoreForCheapest(pricing)
  };
}

async function main() {
  const payload = readPayload();
  const input = payload.input || {};
  const modelQuery = normalizeText(input.model_name);
  const endpointType = normalizeText(input.endpoint_type);
  const tagKeyword = normalizeText(input.tag_keyword);
  const limit = Math.max(1, Math.min(50, numberValue(input.limit, 10)));

  const response = await fetch(PRICING_URL, { headers: { "Accept": "application/json" } });
  if (!response.ok) {
    throw new Error(`pricing request failed: HTTP ${response.status}`);
  }
  const root = await response.json();
  const models = Array.isArray(root.data) ? root.data : [];

  let filtered = models;
  if (modelQuery) {
    filtered = filtered.filter((model) => normalizeText(model.model_name).includes(modelQuery));
  }
  if (endpointType) {
    filtered = filtered.filter((model) => (model.supported_endpoint_types || []).some((item) => normalizeText(item) === endpointType));
  }
  if (tagKeyword) {
    filtered = filtered.filter((model) => normalizeText(model.tags).includes(tagKeyword));
  }

  const results = filtered
    .map((model) => summarizeModel(model, root, input))
    .sort((a, b) => a.cheapest_score - b.cheapest_score)
    .slice(0, limit);

  console.log(JSON.stringify({
    pricing_version: root.pricing_version || "",
    requested: {
      model_name: input.model_name || "",
      group: input.group || "auto",
      endpoint_type: input.endpoint_type || "",
      tag_keyword: input.tag_keyword || "",
      limit
    },
    group_ratio: root.group_ratio || {},
    usable_group: root.usable_group || {},
    auto_groups: root.auto_groups || [],
    matched_count: filtered.length,
    results
  }));
}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
});
