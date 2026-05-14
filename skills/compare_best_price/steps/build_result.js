const fs = require("fs");

function readInput() {
  const raw = fs.readFileSync(0, "utf8");
  return raw ? JSON.parse(raw) : {};
}

async function main() {
  const payload = readInput();
  const results = (payload.context && payload.context.results) || {};
  const fetched = results.fetch_prices || {};
  const items = Array.isArray(fetched.items) ? fetched.items : [];

  const available = items
    .filter((item) => Number(item.stock || 0) > 0)
    .sort((a, b) => Number(a.price || 0) - Number(b.price || 0));

  console.log(JSON.stringify({
    product_name: fetched.product_name || "",
    lowest: available[0] || null,
    candidates: available.slice(0, 5),
    rule: "只比较有库存商品，按价格从低到高排序。"
  }));
}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
});
