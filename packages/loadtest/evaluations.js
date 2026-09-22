import http from "k6/http";
import { check } from "k6";

const base = __ENV.BASE_URL;
if (!base) {
  throw new Error("BASE_URL is required (make api-url after local-deploy)");
}

// Local Floci runs default to 100 req/s; the 1000 req/s NFR run targets a
// real AWS stack with LOADTEST_RATE=1000.
const rate = Number(__ENV.LOADTEST_RATE || 100);
if (!Number.isInteger(rate) || rate <= 0) {
  throw new Error(`LOADTEST_RATE must be a positive integer, got "${__ENV.LOADTEST_RATE}"`);
}

const batch = JSON.stringify([
  {
    name: "Ana",
    cpf: "39053344705",
    credit_score: 780,
    current_invoice_cents: 50000,
    credit_limit_cents: 500000,
    late_payments: 0,
    monthly_spend_cents: [80000, 90000, 70000],
  },
]);

export const options = {
  scenarios: {
    nfr: {
      executor: "constant-arrival-rate",
      rate,
      timeUnit: "1s",
      duration: "10s",
      // 300 and 800 VUs at 1000 req/s, scaled down with the rate.
      preAllocatedVUs: Math.ceil(rate * 0.3),
      maxVUs: Math.ceil(rate * 0.8),
    },
  },
  thresholds: {
    http_req_duration: ["p(99)<800", "p(95)<1000"],
    http_req_failed: ["rate<0.01"],
  },
};

export default function () {
  const res = http.post(`${base}/evaluations/batch`, batch, {
    headers: { "content-type": "application/json" },
  });
  check(res, {
    "status 202": (r) => r.status === 202,
    "queued": (r) => r.json("queued") === 1,
    "batch_id": (r) => Boolean(r.json("batch_id")),
  });
}
