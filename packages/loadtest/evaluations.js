import http from "k6/http";
import { check } from "k6";

const base = __ENV.BASE_URL;
if (!base) {
  throw new Error("BASE_URL is required (make api-url after local-deploy)");
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
      rate: 1000,
      timeUnit: "1s",
      duration: "10s",
      preAllocatedVUs: 300,
      maxVUs: 800,
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
    "report_id": (r) => Boolean(r.json("report_id")),
  });
}
