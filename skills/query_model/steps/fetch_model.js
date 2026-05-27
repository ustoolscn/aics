const PRICING_URL = "https://cooper-api.com/api/pricing";

function numberValue(value, fallback = 0) {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

// 默认使用自动策略选取可用的分组
function pickEffectiveGroup(model, root) {
  const enableGroups = Array.isArray(model.enable_groups) ? model.enable_groups : [];
  const groupRatio = root.group_ratio || {};

  for (const group of root.auto_groups || []) {
    if (enableGroups.includes(group)) {
      return {
        group_name: group,
        group_ratio: numberValue(groupRatio[group], 1)
      };
    }
  }
  return {
    group_name: "default",
    group_ratio: numberValue(groupRatio.default, 1)
  };
}

function parseBillingExpr(expr, groupRatio) {
  const tiers = [];
  if (!expr) return tiers;

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

function round(value) {
  return Math.round(numberValue(value) * 1_000_000) / 1_000_000;
}

// 核心修改点：将复杂的对象转换为 AI 极易理解的扁平语义化数据
function summarizeModelForAI(model, root) {
  const groupInfo = pickEffectiveGroup(model, root);
  const pricing = calculatePricing(model, groupInfo);

  // 将计费数据翻译为自然语言，方便 AI 直接阅读
  let priceDescription = "";
  if (pricing.billing_type === "per_request") {
    priceDescription = `按次计费: $${pricing.price_per_request_usd}/次`;
  } else if (pricing.billing_type === "tiered_per_million_tokens") {
    const tiersDesc = pricing.tiers.map(t =>
      `[阶段:${t.name}] 输入$${t.input_per_million_tokens_usd}/输出$${t.output_per_million_tokens_usd}`
    ).join(" | ");
    priceDescription = `阶梯计费(每百万Token): ${tiersDesc}`;
  } else {
    priceDescription = `按量计费(每百万Token): 输入 $${pricing.input_per_million_tokens_usd}, 输出 $${pricing.output_per_million_tokens_usd}`;
    if (pricing.cache_input_per_million_tokens_usd > 0) {
      priceDescription += `, 缓存输入 $${pricing.cache_input_per_million_tokens_usd}`;
    }
  }

  return {
    模型名称: model.model_name,
    功能描述: model.description || "无",
    模型标签: model.tags || "无",
    支持的接口格式: (model.supported_endpoint_types || []).join(", ") || "未知",
    系统分配计费组: groupInfo.group_name,
    价格明细: priceDescription
  };
}

async function main() {
  console.log("正在请求最新定价数据...");

  const response = await fetch(PRICING_URL, { headers: { "Accept": "application/json" } });
  if (!response.ok) {
    throw new Error(`API请求失败: HTTP ${response.status}`);
  }

  const root = await response.json();
  const models = Array.isArray(root.data) ? root.data : [];

  // 整理所有模型数据
  const aiFriendlyResults = models.map(model => summarizeModelForAI(model, root));

  // 将结果转换为标准 JSON 字符串打印（缩进为2，最适合交由大模型阅读）
  const outputString = JSON.stringify(aiFriendlyResults, null, 2);

  console.log(outputString);

}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
});