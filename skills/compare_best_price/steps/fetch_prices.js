const fs = require("fs");

function readInput() {
  const raw = fs.readFileSync(0, "utf8");
  return raw ? JSON.parse(raw) : {};
}

async function main() {
  const payload = readInput();
  const input = payload.input || {};
  const productName = input.product_name || input.productName || "";

  // TODO: Replace this mock block with real HTTP requests, page fetching, or internal API calls.
  // The script must print JSON to stdout.
  const items = [
    { name: `${productName} 标准版`, vendor: "渠道 A", price: 1299, stock: 8, url: "https://example.com/a" },
    { name: `${productName} 活动版`, vendor: "渠道 B", price: 1199, stock: 3, url: "https://example.com/b" },
    { name: `${productName} 缺货低价`, vendor: "渠道 C", price: 999, stock: 0, url: "https://example.com/c" }
  ];

  console.log(JSON.stringify({
    product_name: productName,
    items
  }));
}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
});
